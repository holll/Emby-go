package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"emby-go/internal/artwork"
	"emby-go/internal/store"
)

// Opt-in real App/Artplayer/Chromium verification. Deterministic media executables
// produce the sprites through GenerateSprite's real disk+CAS pipeline; actual
// browser playback is a small real MP4 served by the isolated HTTP fixture.
func TestSpritesBrowserMobile(t *testing.T) {
	if os.Getenv("SPRITES_BROWSER") != "1" {
		t.Skip("set SPRITES_BROWSER=1 for isolated mobile browser integration")
	}
	root, err := os.MkdirTemp("/tmp", "emby-sprites-browser-")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("browser evidence: %s", root)
	video := filepath.Join(root, "fixture.mp4")
	if output, err := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=size=64x36:rate=1", "-t", "205", "-c:v", "libx264", "-preset", "ultrafast", "-movflags", "+faststart", video).CombinedOutput(); err != nil {
		t.Fatalf("fixture video: %v %s", err, output)
	}
	media := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.ServeFile(w, r, video) }))
	defer media.Close()
	f := artworkFixture(t)
	before, err := f.a.db.Movie(f.movie)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(before.SourcePath, []byte(media.URL+"/fixture.mp4"), 0600); err != nil {
		t.Fatal(err)
	}
	nfoBefore, err := os.ReadFile(before.NFOPath)
	if err != nil {
		t.Fatal(err)
	}
	spriteSeed(t, f)
	probe, err := f.a.artwork.lookPath("ffprobe")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(probe, []byte("#!/bin/sh\nprintf '205\\n'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	opts, err := f.a.spriteOptions()
	if err != nil {
		t.Fatal(err)
	}
	seeded, err := artwork.GenerateSprite(context.Background(), f.a.db, artwork.SpriteRequest{MovieID: f.movie, IntervalMs: 1000, Overwrite: true}, opts)
	if err != nil || seeded.Manifest.Count != 205 || len(seeded.Manifest.Sheets) != 3 {
		t.Fatalf("multi-sheet seed: %+v %v", seeded, err)
	}
	f.a.sprites.run = func(ctx context.Context, db *store.Store, req artwork.SpriteRequest, o artwork.SpriteOptions) (artwork.SpriteResult, error) {
		if req.IntervalMs == 10000 {
			o.Progress(1, 205)
			<-ctx.Done()
			return artwork.SpriteResult{}, ctx.Err()
		}
		// Browser fixture only: retain three real published sheets independently
		// of the valid library-owned RPC density; production generation is tested separately.
		req.IntervalMs = 1000
		return artwork.GenerateSprite(ctx, db, req, o)
	}
	ordinary, err := f.a.db.CreateUser("sprites-mobile-viewer", "password-123", false)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.a.db.SaveAccessTokenForUser("sprites-browser-ordinary", ordinary.ID, time.Time{}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(f.a.Handler())
	defer server.Close()
	script := filepath.Join(root, "mobile.py")
	if err = os.WriteFile(script, []byte(spritesBrowserPython), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "python3", script, server.URL, f.token, root, fmt.Sprint(f.movie)).CombinedOutput()
	t.Logf("%s", output)
	if err != nil {
		t.Fatalf("browser: %v (evidence %s)", err, root)
	}
	after, err := f.a.db.Movie(f.movie)
	if err != nil {
		t.Fatal(err)
	}
	nfoAfter, err := os.ReadFile(before.NFOPath)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) || string(nfoBefore) != string(nfoAfter) {
		t.Fatal("sprite browser operations changed movie metadata or NFO")
	}
}

const spritesBrowserPython = `import json, os, sys
from playwright.sync_api import sync_playwright, expect
base,token,root,movie_id=sys.argv[1:]
results=[]
with sync_playwright() as p:
 browser=p.chromium.launch(executable_path=os.environ.get('CHROME_BIN','/root/.cache/ms-playwright/chromium-1248/chrome-linux64/chrome'),headless=True,args=['--no-sandbox'])
 for width,height in [(375,812),(768,1024),(1280,900)]:
  ctx=browser.new_context(viewport={'width':width,'height':height},is_mobile=width<1000,has_touch=width<1000,device_scale_factor=1,reduced_motion='reduce')
  ctx.add_init_script('localStorage.setItem("emby_token",'+json.dumps(token)+')')
  page=ctx.new_page(); errors=[]; runs=[]; images=[]
  page.on('pageerror',lambda e:errors.append(str(e)))
  page.on('dialog',lambda d:d.accept())
  page.on('request',lambda r:runs.append(json.loads(r.post_data)) if r.url.endswith('/api/admin/sprites/run') and r.method=='POST' else None)
  page.on('request',lambda r:images.append({'url':r.url,'token':r.headers.get('x-emby-token')}) if '/sprites/sheets/' in r.url else None)
  page.goto(base+'/admin#libraries',wait_until='networkidle')
  library=page.request.get(base+'/api/admin/libraries',headers={'X-Emby-Token':token}).json()['items'][0];library_id=str(library['Id']);library_path=base+'/api/admin/libraries/'+library_id+'/sprites'
  published=page.request.get(base+'/api/items/'+movie_id+'/sprites',headers={'X-Emby-Token':token}).json()['generation']
  page.locator('[data-lib-sprites]').first.click();settings=page.locator('#library-sprite-settings');preset=settings.locator('[data-library-sprite=preset]');save=settings.locator('[data-library-sprite=save]');custom=settings.locator('[data-library-sprite=interval]');save_state=settings.locator('[data-library-sprite=state]')
  preset.select_option('300');save.click();expect(save_state).to_contain_text('已保存：每 300 秒');assert len(runs)==0
  page.reload(wait_until='networkidle');page.locator('[data-lib-sprites]').first.click();expect(preset).to_have_value('300')
  preset.select_option('custom');custom.fill('9');save.click();expect(settings.locator('[data-library-sprite=error]')).to_contain_text('10 至 86400');expect(custom).to_have_value('9')
  custom.fill('37');page.route('**/api/admin/libraries/'+library_id+'/sprites',lambda route:route.fulfill(status=503,content_type='application/json',body='{"error":"private-upstream-secret"}'),times=1);save.click();expect(settings.locator('[data-library-sprite=error]')).to_contain_text('草稿已保留');expect(custom).to_have_value('37');assert 'private-upstream-secret' not in settings.inner_text()
  save.click();expect(save_state).to_contain_text('已保存：每 37 秒');preset.select_option('60');save.click();expect(save_state).to_contain_text('已保存：每 60 秒')
  library_geometry=page.evaluate('({inner:innerWidth,scroll:document.documentElement.scrollWidth,right:document.querySelector("#library-sprite-settings").getBoundingClientRect().right})');assert library_geometry['scroll']<=width and library_geometry['right']<=width,library_geometry
  action_boxes=page.locator('[data-lib-sprites]').first.locator('..').locator('button').evaluate_all('(els)=>els.map(e=>{const r=e.getBoundingClientRect();return {label:e.textContent.trim()||e.title,x:r.x,y:r.y,right:r.right,bottom:r.bottom,width:r.width,height:r.height}})')
  assert all(b['x']>=0 and b['right']<=width for b in action_boxes),action_boxes
  assert max(b['width'] for b in action_boxes)-min(b['width'] for b in action_boxes)<1,action_boxes
  for i,b in enumerate(action_boxes):
   assert b['height']>=44 and abs(b['x']-action_boxes[i%2]['x'])<1,action_boxes
   if i%2: assert abs(b['y']-action_boxes[i-1]['y'])<1 and abs(b['bottom']-action_boxes[i-1]['bottom'])<1,action_boxes
  table_geometry=page.locator('.library-table').evaluate('(e)=>({scroll:e.scrollWidth,client:e.clientWidth})');assert table_geometry['scroll']<=table_geometry['client']+1,table_geometry
  preset_box=preset.bounding_box();save_box=save.bounding_box();assert abs(preset_box['x']-save_box['x'])<1 and abs(preset_box['width']-save_box['width'])<1,(preset_box,save_box)
  library_sizes=settings.locator('button,input,select').evaluate_all('(els)=>els.filter(e=>e.getClientRects().length).map(e=>e.getBoundingClientRect().height)');assert all(v>=44 for v in library_sizes),library_sizes
  page.screenshot(path=os.path.join(root,str(width)+'-library-settings.png'),full_page=True);assert len(runs)==0;assert page.request.get(base+'/api/items/'+movie_id+'/sprites',headers={'X-Emby-Token':token}).json()['generation']==published
  preset.select_option('10');save.click();expect(save_state).to_contain_text('已保存：每 10 秒')
  page.evaluate('location.hash="items"')
  card=page.locator('.wall-card').first;expect(card).to_be_visible();card.locator('.wall-title').click()
  page.locator('.detail-maintenance > summary').click();panel=page.locator('#detail-sprites');panel.scroll_into_view_if_needed()
  generate=panel.locator('[data-sprite=generate]');expect(generate).to_be_enabled();expect(panel.locator('[data-sprite=density]')).to_contain_text('每 10 秒');assert panel.locator('[data-sprite=interval]').count()==0
  page.screenshot(path=os.path.join(root,str(width)+'-maintenance.png'),full_page=True)
  generate.click();expect(panel.locator('[data-sprite=state]')).to_contain_text('处理中')
  task=page.request.get(base+'/api/admin/sprites/status',headers={'X-Emby-Token':token}).json()['task_id']
  page.locator('#detail-close').click();page.wait_for_timeout(1300)
  assert page.request.get(base+'/api/admin/sprites/status',headers={'X-Emby-Token':token}).json()['running']
  card.locator('.wall-title').click();page.locator('.detail-maintenance > summary').click();expect(panel.locator('[data-sprite=state]')).to_contain_text('处理中');expect(generate).to_be_disabled()
  panel.scroll_into_view_if_needed();page.screenshot(path=os.path.join(root,str(width)+'-restored.png'),full_page=True)
  panel.locator('[data-sprite=cancel]').click();expect(panel.locator('[data-sprite=state]')).to_contain_text('已取消');expect(generate).to_be_enabled()
  expect(panel.locator('[data-sprite=density]')).to_contain_text('每 10 秒')
  def lose_accepted(route):
   response=route.fetch();assert response.status==202;route.abort('failed')
  page.route('**/api/admin/sprites/run',lose_accepted,times=1);generate.click();expect(panel.locator('[data-sprite=state]')).to_contain_text('提交结果未确认');expect(generate).to_be_disabled()
  page.screenshot(path=os.path.join(root,str(width)+'-uncertain.png'),full_page=True)
  page.locator('#detail-close').click();card.locator('.wall-title').click();page.locator('.detail-maintenance > summary').click();expect(panel.locator('[data-sprite=state]')).to_contain_text('处理中')
  panel.locator('[data-sprite=cancel]').click();expect(panel.locator('[data-sprite=state]')).to_contain_text('已取消')
  panel.locator('a[href="#libraries"]').click();expect(page.locator('[data-lib-sprites]').first).to_be_visible();page.locator('[data-lib-sprites]').first.click();preset.select_option('60');save.click();expect(save_state).to_contain_text('已保存：每 60 秒')
  page.evaluate('location.hash="items"');expect(card).to_be_visible();card.locator('.wall-title').click();page.locator('.detail-maintenance > summary').click();expect(panel.locator('[data-sprite=density]')).to_contain_text('每 60 秒');panel.locator('[data-sprite=overwrite]').check();generate.click();expect(panel.locator('[data-sprite=state]')).to_contain_text('已结束',timeout=45000)
  manifest=page.request.get(base+'/api/items/'+movie_id+'/sprites',headers={'X-Emby-Token':token}).json();assert manifest['count']==205 and manifest['sheet_count']==3,manifest
  geometry=page.evaluate('({inner:innerWidth,scroll:document.documentElement.scrollWidth,right:document.querySelector("#detail-sprites").getBoundingClientRect().right})');assert geometry['inner']<=width+1 and geometry['scroll']<=width and geometry['right']<=width+1,geometry
  sizes=panel.locator('button,input:not([type=checkbox])').evaluate_all('(els)=>els.filter(e=>e.getClientRects().length).map(e=>e.getBoundingClientRect().height)');assert all(v>=44 for v in sizes),sizes
  page.locator('#detail-play').click();rail=page.locator('.sprites-timeline');expect(rail).to_be_visible();slider=rail.locator('input');expect(slider).to_be_enabled()
  page.evaluate('()=>{window.revoked=[];window.created=[];const oldRevoke=URL.revokeObjectURL.bind(URL),oldCreate=URL.createObjectURL.bind(URL);URL.revokeObjectURL=u=>{revoked.push(u);oldRevoke(u)};URL.createObjectURL=b=>{const u=oldCreate(b);created.push(u);return u}}')
  slider.focus();slider.press('ArrowRight');expect(rail.locator('output')).to_have_text('00:00:01');expect(page.locator('.sprites-player-preview')).to_be_visible()
  slider.evaluate('(e)=>{e.value="166000";e.dispatchEvent(new Event("input",{bubbles:true}))}')
  expect(rail.locator('output')).to_have_text('00:02:46');expect(page.locator('.sprites-player-preview')).to_be_visible()
  picture=page.locator('.sprites-player-picture');expect(picture).to_have_css('background-position','-960px -540px')
  slider.evaluate('(e)=>{e.value="204000";e.dispatchEvent(new Event("input",{bubbles:true}))}');expect(rail.locator('output')).to_have_text('00:03:24');expect(picture).to_have_css('background-position','-640px 0px')
  assert picture.evaluate('(e)=>e.style.backgroundImage').startswith('url("blob:')
  assert slider.bounding_box()['height']>=44 and rail.locator('button').bounding_box()['height']>=44
  page.screenshot(path=os.path.join(root,str(width)+'-timeline-sheet3.png'),full_page=True)
  rail.locator('button').click();page.wait_for_function('artInstance.currentTime>=203 && artInstance.currentTime<=205')
  if width>=1000:
   bar=page.locator('.art-progress');box=bar.bounding_box();page.mouse.move(box['x']+box['width']*0.51,box['y']+box['height']/2);expect(page.locator('.sprites-player-preview')).to_be_visible();page.screenshot(path=os.path.join(root,str(width)+'-pointer-hover.png'),full_page=True);page.mouse.move(0,0);expect(page.locator('.sprites-player-preview')).to_be_hidden()
  assert len(images)>=3 and all(i['token']==token and 'api_key=' not in i['url'] for i in images),images
  page.locator('#player-close').click();assert page.locator('.sprites-timeline').count()==0;assert page.locator('.sprites-player-preview').count()==0
  assert page.evaluate('created.length>0 && created.every(u=>revoked.includes(u))')
  bad=json.loads(json.dumps(manifest));bad['sheets'][0]['url']='https://evil.invalid/private?api_key=secret'
  page.route('**/api/items/'+movie_id+'/sprites',lambda route:route.fulfill(status=200,content_type='application/json',body=json.dumps(bad)),times=1)
  card.locator('.wall-title').click();page.locator('#detail-play').click();expect(rail).to_be_visible();expect(rail.locator('input')).to_be_disabled();expect(rail.locator('[data-preview-error]')).to_contain_text('不影响播放');page.locator('#player-close').click()
  page.route('**/api/items/'+movie_id+'/sprites',lambda route:route.fulfill(status=404,content_type='application/json',body='{}'),times=1)
  card.locator('.wall-title').click()
  with page.expect_response(lambda response:response.url.endswith('/api/items/'+movie_id+'/sprites')):
   page.locator('#detail-play').click()
  expect(rail).to_be_hidden();assert page.locator('.art-video').count()==1;page.locator('#player-close').click()
  assert len(runs)==3 and runs[-1]['overwrite'] is True and all(set(run)=={'movie_ids','overwrite'} for run in runs),runs
  assert not errors,errors
  results.append({'viewport':[width,height],'restored_task':task,'runs':runs,'manifest_count':manifest['count'],'sheet_count':manifest['sheet_count'],'authenticated_sheet_requests':len(images),'blob_cleanup':True,'range_keyboard':True,'exact_multi_sheet_crop':True,'pointer_hover':width>=1000,'page_errors':errors,'geometry':geometry,'control_heights':sizes,'library_settings_geometry':library_geometry,'library_control_heights':library_sizes,'library_action_boxes':action_boxes,'library_settings_saved_without_generation':True,'library_draft_error_recovery':True,'detail_inherited_interval_seconds':60})
  ctx.close()
 ctx=browser.new_context(viewport={'width':812,'height':375},is_mobile=True,has_touch=True,reduced_motion='reduce');ctx.add_init_script('localStorage.setItem("emby_token",'+json.dumps(token)+')');page=ctx.new_page();page.goto(base+'/admin#items',wait_until='networkidle');page.locator('.wall-title').first.click();page.locator('#detail-play').click();expect(page.locator('.sprites-timeline')).to_be_visible();landscape_slider=page.locator('.sprites-timeline input');landscape_slider.focus();landscape_slider.press('ArrowRight');expect(page.locator('.sprites-player-preview')).to_be_visible()
 landscape=page.evaluate('(()=>{const b=document.querySelector(".sprites-player-preview").getBoundingClientRect(),p=document.querySelector(".sprites-player-preview").parentElement.getBoundingClientRect(),r=document.querySelector(".sprites-timeline").getBoundingClientRect();return {top:b.top,playerTop:p.top,bottom:b.bottom,playerBottom:p.bottom,railBottom:r.bottom,inner:innerWidth,scroll:document.documentElement.scrollWidth}})()');assert landscape['top']>=landscape['playerTop'] and landscape['bottom']<=landscape['playerBottom'] and landscape['railBottom']<=375 and landscape['scroll']<=812,landscape;page.screenshot(path=os.path.join(root,'812-landscape-timeline.png'),full_page=True);results.append({'viewport':[812,375],'preview_unclipped':True,'geometry':landscape});ctx.close()
 ctx=browser.new_context(viewport={'width':375,'height':812},is_mobile=True,has_touch=True);ctx.add_init_script('localStorage.setItem("emby_token","sprites-browser-ordinary")');page=ctx.new_page();page.goto(base+'/admin#items',wait_until='networkidle');assert page.locator('#detail-sprites').count()==0;assert page.request.get(base+'/api/admin/sprites/capabilities',headers={'X-Emby-Token':'sprites-browser-ordinary'}).status==403;ctx.close();browser.close()
with open(os.path.join(root,'results.json'),'w') as f:json.dump({'real_app_and_api':True,'real_video_playback':True,'mock_sprite_media_executables':True,'real_sprite_disk_and_state_publication':True,'ordinary_admin_api_status':403,'results':results},f,ensure_ascii=False,indent=2)
print(json.dumps({'ok':True,'evidence':root,'viewports':[[375,812],[768,1024],[1280,900]],'mock_sprite_media_executables':True}))
`
