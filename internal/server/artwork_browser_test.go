package server

import (
	"context"
	"fmt"
	"image"
	"image/png"
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

// Opt-in, isolated browser integration. Real App/router/auth/status/cancel APIs;
// only upstream candidate data and media execution are mocked. All runtime
// evidence is written under /tmp, never into the repository or reports.
func TestArtworkBrowserMobile(t *testing.T) {
	if os.Getenv("ARTWORK_BROWSER") != "1" {
		t.Skip("set ARTWORK_BROWSER=1 to run isolated Chromium mobile integration")
	}
	root, err := os.MkdirTemp("/tmp", "emby-artwork-browser-")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("browser evidence: %s", root)
	f := artworkFixture(t)
	before, err := f.a.db.Movie(f.movie)
	if err != nil {
		t.Fatal(err)
	}
	nfoBefore, err := os.ReadFile(before.NFOPath)
	if err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/movies/search" {
			fmt.Fprint(w, `{"data":[{"provider":"Fixture","id":"cover-1","title":"候选 <img onerror=window.xss=true> 超长标题 ABC-123","number":"ABC-123"}]}`)
		} else if r.URL.Path == "/v1/images/primary/Fixture/cover-1" || r.URL.Path == "/v1/images/thumb/Fixture/cover-1" {
			w.Header().Set("Content-Type", "image/png")
			if err := png.Encode(w, image.NewRGBA(image.Rect(0, 0, 88, 124))); err != nil {
				t.Error(err)
			}
		} else {
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	for key, value := range map[string]string{settingMetatubeURL: upstream.URL, settingMetatubeToken: "fixture-only-token"} {
		if err = f.a.db.SetSetting(key, value); err != nil {
			t.Fatal(err)
		}
	}
	f.a.artwork.run = func(ctx context.Context, _ *store.Store, _ artwork.Request, _ artwork.Options) (artwork.Result, error) {
		<-ctx.Done()
		return artwork.Result{}, ctx.Err()
	}
	ordinary, err := f.a.db.CreateUser("mobile-viewer", "password-123", false)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.a.db.SaveAccessTokenForUser("browser-ordinary", ordinary.ID, time.Time{}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(f.a.Handler())
	defer server.Close()
	script := filepath.Join(root, "mobile.py")
	if err = os.WriteFile(script, []byte(artworkBrowserPython), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "python3", script, server.URL, f.token, root)
	output, err := cmd.CombinedOutput()
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
		t.Fatal("browser artwork cancellation changed metadata or NFO")
	}
}

const artworkBrowserPython = `import json, os, sys
from playwright.sync_api import sync_playwright, expect
base, token, root = sys.argv[1:]
results=[]
with sync_playwright() as p:
 browser=p.chromium.launch(executable_path=os.environ.get('CHROME_BIN','/root/.cache/ms-playwright/chromium-1248/chrome-linux64/chrome'),headless=True,args=['--no-sandbox'])
 for width,height in [(375,812),(768,1024)]:
  ctx=browser.new_context(viewport={'width':width,'height':height},is_mobile=True,has_touch=True,device_scale_factor=1,reduced_motion='reduce')
  ctx.add_init_script('localStorage.setItem("emby_token",'+json.dumps(token)+')')
  page=ctx.new_page(); errors=[]; runs=[]
  page.on('pageerror',lambda error: errors.append(str(error)))
  page.on('dialog',lambda dialog: dialog.accept())
  page.on('request',lambda req: runs.append(json.loads(req.post_data)) if req.url.endswith('/api/admin/artwork/run') and req.method=='POST' else None)
  page.goto(base+'/admin#items',wait_until='networkidle')
  card=page.locator('.wall-card').first
  expect(card).to_be_visible(); card.click()
  page.locator('.detail-maintenance > summary').click()
  panel=page.locator('#detail-artwork')
  expect(panel).to_be_visible()
  shot=panel.locator('[data-art=screenshot]'); expect(shot).to_be_enabled()
  panel.scroll_into_view_if_needed()
  page.screenshot(path=os.path.join(root,str(width)+'-initial.png'),full_page=True)
  panel.locator('summary').nth(1).click()
  panel.locator('[data-art=query]').fill('ABC')
  with page.expect_response(lambda response:'/api/admin/scrape/image?' in response.url) as image_response:
   panel.locator('[data-art=search]').click()
  assert image_response.value.status==200
  assert image_response.value.request.headers.get('x-emby-token')==token
  assert 'api_key=' not in image_response.value.url
  candidate=panel.locator('[data-candidate]').first
  expect(candidate).to_be_visible()
  page.wait_for_function('document.querySelector("[data-candidate] img")?.naturalWidth > 0')
  assert candidate.locator('img').get_attribute('src').startswith('blob:')
  expect(candidate).to_contain_text('<img onerror=window.xss=true>')
  assert page.evaluate('window.xss !== true')
  page.screenshot(path=os.path.join(root,str(width)+'-candidates.png'),full_page=True)
  candidate.click()
  expect(panel.locator('[data-art=state]')).to_contain_text('处理中')
  task_id=page.request.get(base+'/api/admin/artwork/status',headers={'X-Emby-Token':token}).json()['task_id']
  page.locator('#detail-close').click()
  page.wait_for_timeout(1400)
  assert page.request.get(base+'/api/admin/artwork/status',headers={'X-Emby-Token':token}).json()['running']
  card.click(); page.locator('.detail-maintenance > summary').click()
  expect(panel.locator('[data-art=state]')).to_contain_text('处理中')
  assert page.request.get(base+'/api/admin/artwork/status',headers={'X-Emby-Token':token}).json()['task_id']==task_id
  expect(shot).to_be_disabled()
  panel.scroll_into_view_if_needed()
  page.screenshot(path=os.path.join(root,str(width)+'-restored-running.png'),full_page=True)
  panel.locator('[data-art=cancel]').click()
  expect(panel.locator('[data-art=state]')).to_contain_text('已取消')
  expect(shot).to_be_enabled()
  shot.click(); expect(panel.locator('[data-art=state]')).to_contain_text('处理中')
  panel.locator('[data-art=cancel]').click()
  expect(panel.locator('[data-art=state]')).to_contain_text('已取消')
  page.screenshot(path=os.path.join(root,str(width)+'-cancelled.png'),full_page=True)
  # Real server accepts, then deliberately discard the response to emulate transport loss.
  def lose_accepted_response(route):
   response=route.fetch()
   assert response.status==202
   route.abort('failed')
  page.route('**/api/admin/artwork/run',lose_accepted_response,times=1)
  shot.click()
  expect(panel.locator('[data-art=state]')).to_contain_text('提交结果未确认')
  expect(shot).to_be_disabled()
  page.screenshot(path=os.path.join(root,str(width)+'-uncertain.png'),full_page=True)
  page.locator('#detail-close').click(); card.click()
  page.locator('.detail-maintenance > summary').click()
  expect(panel.locator('[data-art=state]')).to_contain_text('处理中')
  panel.locator('[data-art=cancel]').click()
  expect(panel.locator('[data-art=state]')).to_contain_text('已取消')
  sizes=panel.locator('button,input:not([type=checkbox]),select,summary').evaluate_all('(els)=>els.filter(e=>e.getClientRects().length).map(e=>({tag:e.tagName,height:e.getBoundingClientRect().height,width:e.getBoundingClientRect().width}))')
  assert all(s['height']>=44 for s in sizes),sizes
  geometry=page.evaluate('({inner:innerWidth,client:document.documentElement.clientWidth,scroll:document.documentElement.scrollWidth,visual:visualViewport.width,drawer:document.querySelector("#detail-drawer").getBoundingClientRect().width,panel:document.querySelector("#detail-artwork").getBoundingClientRect().width,panelRight:document.querySelector("#detail-artwork").getBoundingClientRect().right,meta:document.querySelector("meta[name=viewport]").content})')
  print(json.dumps({'viewport_geometry':geometry,'requested_width':width}),flush=True)
  assert geometry['inner']<=width+1,geometry
  assert geometry['drawer']<=width+1,geometry
  assert geometry['panelRight']<=width+1,geometry
  assert all(s['width']<=width for s in sizes),sizes
  assert geometry['scroll']<=geometry['inner'],geometry
  assert len(runs)==3,runs
  assert runs[0]['mode']=='provider' and runs[0]['provider']=='Fixture' and runs[0]['overwrite'] is True
  assert runs[1]['mode']=='screenshot' and runs[1]['position_percent']==10 and runs[1]['overwrite'] is False
  assert not errors,errors
  shot.focus(); assert shot.evaluate('(e)=>document.activeElement===e')
  results.append({'viewport':[width,height],'runs':runs,'restored_task':task_id,'visible_controls':sizes,'page_errors':errors,'overflow':False})
  ctx.close()
 ctx=browser.new_context(viewport={'width':375,'height':812},is_mobile=True,has_touch=True)
 ctx.add_init_script('localStorage.setItem("emby_token","browser-ordinary")')
 page=ctx.new_page(); page.goto(base+'/admin#items',wait_until='networkidle')
 expect(page).to_have_url(base+'/admin#settings')
 assert page.locator('.wall-card').count()==0
 assert page.locator('#detail-artwork').count()==0
 assert page.request.get(base+'/api/admin/artwork/capabilities',headers={'X-Emby-Token':'browser-ordinary'}).status==403
 page.screenshot(path=os.path.join(root,'375-ordinary-no-artwork.png'),full_page=True)
 browser.close()
with open(os.path.join(root,'results.json'),'w') as f: json.dump({'mock_media_execution':True,'mock_candidate_upstream':True,'real_app_and_api':True,'ordinary_controls_absent':True,'ordinary_api_status':403,'results':results},f,ensure_ascii=False,indent=2)
print(json.dumps({'ok':True,'evidence':root,'viewports':[[375,812],[768,1024]],'mock_media_execution':True}))
`
