package server

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
)

// Opt-in Chromium regression. Every URL is fulfilled from read-only local web
// assets or in-memory fixtures; no server, credentials or production writes.
func TestNov10Browser(t *testing.T) {
	if os.Getenv("NOV10_BROWSER") != "1" {
		t.Skip("set NOV10_BROWSER=1; requires Python playwright and Chromium")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "python3", "-c", nov10BrowserPython).CombinedOutput()
	t.Logf("%s", out)
	if err != nil {
		t.Fatal(err)
	}
}

const nov10BrowserPython = `
import json, mimetypes, os, tempfile
from pathlib import Path
from urllib.parse import urlparse, parse_qs
from playwright.sync_api import sync_playwright, expect

root = Path(tempfile.mkdtemp(prefix='emby-nov10-browser-', dir='/tmp'))
print('browser evidence: '+str(root), flush=True)
web = Path('web').resolve()
long_name = 'Library' + 'LongUnbrokenFixture' * 14
libraries = [{'Id': str(i), 'Name': long_name + str(i), 'Path': '/media/' + 'nested/' * 32 + 'movie', 'count': 123, 'sprite_interval_seconds': 10} for i in range(1, 4)]
results = []
with sync_playwright() as p:
    browser = p.chromium.launch(headless=True, args=['--no-sandbox'], **({'executable_path': os.environ['CHROME_BIN']} if os.environ.get('CHROME_BIN') else {}))
    # 640x450 CSS pixels / DPR2 is desktop 1280x900 at 200% zoom's
    # equivalent layout viewport, NOT DPR-only emulation or CSS transform.
    for width, height, dpr, label in [(375,812,1,'375'), (768,1024,1,'768'), (1280,900,1,'1280'), (640,450,2,'1280-at-200pct')]:
        ctx = browser.new_context(viewport={'width':width,'height':height}, device_scale_factor=dpr, reduced_motion='reduce')
        ctx.add_init_script('localStorage.setItem("emby_token","offline-fixture")')
        page = ctx.new_page()
        errors, unexpected, held, calls = [], [], [], []
        state = {'hold_lists': False, 'hold_keys': False}
        page.on('pageerror', lambda error: errors.append(str(error)))
        def reply(route, data, status=200):
            route.fulfill(status=status, content_type='application/json', body=json.dumps(data))
        def route_all(route):
            request = route.request
            parsed = urlparse(request.url)
            path = parsed.path
            calls.append({'path':path,'query':parsed.query,'method':request.method,'body':request.post_data,'headers':request.headers})
            if parsed.netloc != 'nov10.invalid':
                unexpected.append(request.url); route.abort(); return
            if path == '/admin':
                route.fulfill(content_type='text/html', body=(web/'index.html').read_text()); return
            if path.startswith('/web/'):
                asset = (web / path.removeprefix('/web/')).resolve()
                if web in asset.parents and asset.is_file():
                    route.fulfill(content_type=mimetypes.guess_type(str(asset))[0] or 'application/octet-stream', body=asset.read_bytes()); return
            if path == '/Users/Me':
                reply(route, {'Name':'offline-admin','Policy':{'IsAdministrator':True}}); return
            if path.startswith('/api/admin/'):
                name = path.removeprefix('/api/admin')
                if name in ['/login-events','/playback-events']:
                    if state['hold_lists']:
                        held.append(route); return
                    start = int(parse_qs(parsed.query).get('start',['0'])[0])
                    reply(route, {'events':[{'name':'login-'+str(start),'ip':'192.0.2.1','success':True}], 'items':[{'movie_title':'play-'+str(start),'user_name':'fixture'}], 'total':80}); return
                if name == '/apikeys' and request.method == 'POST' or name.endswith('/rotate'):
                    if state['hold_keys']:
                        held.append(route); return
                    unexpected.append('unexpected unheld write '+path); route.abort(); return
                fixtures = {
                    '/libraries': {'items':libraries,'total':3},
                    '/apikeys': {'items':[{'id':'ak_first','name':long_name,'key_prefix':'a'*100,'version':3,'created_at':'2026-11-10T00:00:00Z'}]},
                    '/users': {'users':[{'id':2,'name':'User'+long_name,'is_admin':False,'library_ids':['1']}]},
                    '/recovery': {'items':[]},
                    '/security/ip-blocklist': {'items':[], 'max_rules':256,'client_ip':'127.0.0.1','total_blocked':0},
                    '/logs/settings': {'retention_days':7,'config_etag':'fixture'},
                    '/image-cache/settings': {'max_bytes':1073741824,'config_etag':'fixture'},
                    '/webhook/targets': {'targets':[],'multi_target':True,'config_etag':'fixture'},
                    '/webhook/deliveries': {'items':[]},
                    '/scan/progress': {'running':False}, '/update': {'current_version':'fixture','enabled':False}
                }
                if name in fixtures and request.method == 'GET':
                    reply(route, fixtures[name]); return
            unexpected.append(request.method+' '+request.url)
            route.abort()
        page.route('**/*', route_all)
        def geometry(stage):
            data = page.evaluate('''() => ({inner:innerWidth, scroll:document.documentElement.scrollWidth,
                overflow:[...document.querySelectorAll('#content .panel,#content .library-card,#content .library-card-actions,#content .check-grid,#content .apikey-panel')]
                .filter(e=>e.getClientRects().length).filter(e=>e.getBoundingClientRect().right>innerWidth+1 || e.getBoundingClientRect().left < -1)
                .map(e=>({class:e.className,right:e.getBoundingClientRect().right}))})''')
            page.screenshot(path=str(root/(label+'-'+stage+'.png')), full_page=True)
            assert data['inner'] == width and data['scroll'] <= width and not data['overflow'], (label,stage,data)
            return data
        def keyboard_activate(selector):
            page.locator(selector).focus(); page.keyboard.press('Enter')
        page.goto('https://nov10.invalid/admin#libraries')
        expect(page.locator('.library-card')).to_have_count(3)
        geometry('libraries')
        keyboard_activate('[data-lib-add]')
        expect(page.locator('#lib-name')).to_be_focused()
        trigger = page.locator('[data-lib-sprites]').first
        trigger.focus(); page.keyboard.press('Tab')
        expect(page.locator('[data-lib-scan]').first).to_be_focused()
        page.keyboard.press('Shift+Tab'); expect(trigger).to_be_focused()
        page.keyboard.press('Enter')
        expect(page.locator('#library-sprite-settings')).to_be_visible()
        assert page.locator('#library-sprite-settings').evaluate('(e)=>e.matches(":modal")')
        page.keyboard.press('Escape')
        expect(page.locator('#library-sprite-settings')).to_be_hidden()
        expect(trigger).to_be_focused()
        # Hash navigation uses the real application's route handlers.
        page.evaluate('location.hash="settings"')
        expect(page.locator('[data-settings-category="users"]')).to_be_visible()
        keyboard_activate('[data-settings-category="users"]')
        expect(page.locator('#apikey-form')).to_be_visible()
        checkbox = page.locator('[data-user-lib="2"]').first
        before = checkbox.is_checked(); checkbox.focus(); page.keyboard.press('Space')
        assert checkbox.is_checked() != before
        geometry('settings-long-strings')
        state['hold_keys'] = True
        page.locator('#key-name').fill('fixture key')
        page.locator('#key-name').press('Enter')
        expect(page.locator('#apikey-submit')).to_be_disabled()
        expect(page.locator('#apikey-submit')).to_have_text('创建中…')
        assert len(held) == 1
        page.locator('#apikey-form').evaluate('(e)=>{e.requestSubmit();e.requestSubmit()}')
        assert len(held) == 1
        reply(held.pop(), {'error':'offline create failed'}, 500)
        expect(page.locator('#apikey-submit')).to_be_enabled()
        expect(page.locator('#key-name')).to_have_value('fixture key')
        keyboard_activate('#apikey-submit')
        page.wait_for_function('document.querySelector("#apikey-submit").disabled')
        raw_key = 'fixture-'+'x'*256
        reply(held.pop(), {'key':raw_key})
        expect(page.locator('#created-key')).to_have_text(raw_key)
        assert page.locator('#created-key').bounding_box()['width'] >= page.locator('.apikey-panel').bounding_box()['width'] * .7, 'new key must use its own readable full-width row'
        geometry('created-key')
        keyboard_activate('[data-key-rotate]')
        expect(page.locator('#key-rotate-reason')).to_be_focused()
        keyboard_activate('#key-rotate-cancel')
        expect(page.locator('#key-rotate-form')).to_be_hidden()
        assert not held
        keyboard_activate('[data-key-rotate]')
        page.locator('#key-rotate-reason').fill('offline fixture')
        page.locator('#key-rotate-reason').press('Enter')
        expect(page.locator('#key-rotate-submit')).to_be_disabled()
        expect(page.locator('#key-rotate-submit')).to_have_text('轮转中…')
        expect(page.locator('#key-rotate-cancel')).to_be_disabled()
        page.locator('#key-rotate-form').evaluate('(e)=>e.requestSubmit()')
        assert len(held) == 1
        assert held[0].request.headers['if-match'] == '"ak-first-3"'
        reply(held.pop(), {'error':'offline rotation failed'}, 500)
        expect(page.locator('#key-rotate-submit')).to_be_enabled()
        expect(page.locator('#key-rotate-reason')).to_have_value('offline fixture')
        geometry('rotation')
        page.evaluate('location.hash="firewall"')
        expect(page.locator('#firewall-login-body')).to_contain_text('login-0')
        expect(page.locator('#firewall-play-body')).to_contain_text('play-0')
        state['hold_lists'] = True
        for kind in ['login','play']:
            next_button = '#firewall-'+kind+'-body [data-page-step="1"]'
            keyboard_activate(next_button); page.wait_for_function('true')
            keyboard_activate(next_button)
            expect(page.locator(next_button)).to_be_enabled()
            assert len(held) == 2
            old, new = held[:]; held.clear()
            assert 'start=20' in old.request.url and 'start=40' in new.request.url
            reply(new, {'events':[{'name':'latest-third'}],'items':[{'movie_title':'latest-third'}],'total':80})
            body = page.locator('#firewall-'+kind+'-body')
            expect(body).to_contain_text('latest-third'); expect(body).to_contain_text('第 3 / 4 页')
            reply(old, {'events':[],'items':[],'total':80})
            # Browser event loop barrier ensures the older fetch callback had time to run.
            page.evaluate('() => new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)))')
            expect(body).to_contain_text('latest-third'); expect(body).to_contain_text('第 3 / 4 页')
            assert not held
        state['hold_lists'] = False
        page.locator('#firewall-login-ip').fill('192.0.2.1')
        page.locator('#firewall-login-result').select_option('failed')
        page.locator('#firewall-login-ip').press('Enter')
        expect(page.locator('#firewall-login-body')).to_contain_text('第 1 / 4 页')
        keyboard_activate('#firewall-login-reset')
        expect(page.locator('#firewall-login-ip')).to_have_value('')
        expect(page.locator('#firewall-login-result')).to_have_value('all')
        expect(page.locator('#firewall-login-ip')).to_be_focused()
        page.locator('#firewall-login-ip').press('Enter')
        page.evaluate('() => new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)))')
        latest = [c for c in calls if c['path'].endswith('/login-events')][-1]
        assert parse_qs(latest['query']) == {'start':['0'],'limit':['20'],'result':['all']}, latest
        geometry('firewall')
        assert not errors and not unexpected, {'errors':errors,'unexpected':unexpected}
        results.append({'case':label,'css_viewport':[width,height],'dpr':dpr,'screenshots':5,'keyboard':True,'deferred_fetch_interactions':True,'page_errors':errors,'unexpected_requests':unexpected})
        ctx.close()
    browser.close()
(root/'results.json').write_text(json.dumps(results, ensure_ascii=False, indent=2))
print(json.dumps({'evidence':str(root),'results':results}, ensure_ascii=False))
`
