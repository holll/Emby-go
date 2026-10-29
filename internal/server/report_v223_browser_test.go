package server

import (
	"context"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Opt-in real Chromium verification for the V2.2.2 report fixes. Everything except
// the browser is the real application: real router, auth tokens, activity log,
// blocklist persistence and probe store. No product behaviour is stubbed here.
func TestReportFindingsBrowserMobile(t *testing.T) {
	if os.Getenv("REPORT_BROWSER") != "1" {
		t.Skip("set REPORT_BROWSER=1 for isolated report-fix browser verification")
	}
	root, err := os.MkdirTemp("/tmp", "emby-v223-report-browser-")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("browser evidence: %s", root)
	f := artworkFixture(t)
	ordinary, err := f.a.db.CreateUser("report-ordinary-user", "password-123", false)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.a.db.SaveAccessTokenForUser("report-ordinary", ordinary.ID, time.Time{}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(f.a.Handler())
	defer server.Close()
	script := filepath.Join(root, "report.py")
	if err = os.WriteFile(script, []byte(reportFindingsPython), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "python3", script, server.URL, f.token, root).CombinedOutput()
	t.Logf("%s", output)
	if err != nil {
		t.Fatalf("browser: %v (evidence %s)", err, root)
	}
}

const reportFindingsPython = `import json, os, sys
from playwright.sync_api import sync_playwright, expect
base,token,root=sys.argv[1:]
results=[]

def visible_controls_size(page, selector):
    return page.locator(selector).evaluate_all('(els)=>els.filter(e=>e.getClientRects().length&&!e.closest("[hidden]")).map(e=>e.getBoundingClientRect().height)')

with sync_playwright() as p:
    browser=p.chromium.launch(executable_path=os.environ.get('CHROME_BIN','/root/.cache/ms-playwright/chromium-1248/chrome-linux64/chrome'),headless=True,args=['--no-sandbox'])
    for width,height in [(375,812),(768,1024),(1280,900)]:
        ctx=browser.new_context(viewport={'width':width,'height':height},is_mobile=width<1000,has_touch=width<1000,device_scale_factor=1,reduced_motion='reduce')
        ctx.add_init_script('localStorage.setItem("emby_token",'+json.dumps(token)+')')
        page=ctx.new_page(); errors=[]; created=[]
        page.on('pageerror',lambda e:errors.append(str(e)))
        page.on('dialog',lambda d:d.accept())
        page.on('request',lambda r:created.append(json.loads(r.post_data)) if r.url.endswith('/api/admin/scheduled') and r.method=='POST' else None)
        page.goto(base+'/admin#libraries',wait_until='networkidle')
        # (1) 原生下拉选项在深色主题下可读：选中项与展开列表同样是不透明深底白字。
        page.evaluate('location.hash="scheduled"');page.locator('details:has(#sched-form) > summary').click();form=page.locator('#sched-form');expect(form).to_be_visible()
        option=page.locator('#s-type option').first.evaluate('(e)=>{const s=getComputedStyle(e);return {bg:s.backgroundColor,color:s.color}}')
        assert option['bg'] in ('rgb(32, 32, 36)','rgb(32,32,36)'),option
        assert option['color'] in ('rgb(255, 255, 255)','rgb(255,255,255)'),option
        # (3) 雪碧图计划类型：只显示自己的参数组，沿用媒体库密度，默认不覆盖。
        page.locator('#s-type').select_option('sprites')
        expect(page.locator('[data-show="sprites"]').first).to_be_visible()
        expect(page.locator('[data-show="probe"]').first).to_be_hidden()
        expect(page.locator('[data-show="translate"]').first).to_be_hidden()
        page.locator('#s-name').fill('雪碧图夜间任务')
        page.locator('#s-cron').fill('0 4 * * *')
        page.locator('#s-enabled').uncheck()
        page.locator('#sched-submit').click()
        expect(page.locator('#sched-submit')).to_be_enabled()
        expect(page.locator('body')).to_contain_text('雪碧图生成')
        assert created and created[-1]['type']=='sprites' and created[-1]['params']['limit']==0 and created[-1]['params']['overwrite'] is False,created
        assert 'interval_seconds' not in created[-1]['params'],created
        geometry=page.evaluate('({inner:innerWidth,scroll:document.documentElement.scrollWidth})');assert geometry['scroll']<=width,geometry
        if width<1000:
            sizes=visible_controls_size(page,'#sched-form input:not([type=checkbox]),#sched-form select,#sched-form button')
            assert sizes and all(v>=44 for v in sizes),sizes
        # (4) 日志自动刷新不再出现“新日志”提示条，暂停仍可用。
        page.evaluate('location.hash="logs"');page.wait_for_timeout(300)
        assert page.locator('#dl-newbar').count()==0
        pause=page.locator('#dl-pause');expect(pause).to_be_visible();pause.click();expect(pause).to_have_text('继续');pause.click();expect(pause).to_have_text('暂停')
        assert page.evaluate('document.documentElement.scrollWidth')<=width
        # (5) 活动记录按精确 IP 与结果筛选；真实失败登录事件由真实接口产生。
        login=page.request.post(base+'/Users/AuthenticateByName',data=json.dumps({'Username':'admin','Pw':'report-wrong-password'}))
        assert login.status==401,login.status
        page.evaluate('location.hash="activity"');page.wait_for_timeout(300)
        page.locator('#audit-ip').fill('127.0.0.1');page.locator('#audit-result').select_option('failed');page.locator('#audit-filter button[type=submit]').click()
        page.wait_for_timeout(500);failed_rows=page.locator('main table tbody tr').count();assert failed_rows>=1,failed_rows
        assert '失败' in page.locator('main').inner_text()
        page.locator('#audit-ip').fill('198.51.100.7');page.locator('#audit-filter button[type=submit]').click();page.wait_for_timeout(500)
        assert page.locator('main table tbody tr').count()==0
        page.locator('#audit-ip').fill('127.0.0.1');page.locator('#audit-result').select_option('success');page.locator('#audit-filter button[type=submit]').click();page.wait_for_timeout(500)
        assert page.locator('main table tbody tr').count()==0
        page.locator('#audit-reset').click();page.wait_for_timeout(400)
        if width<1000:
            sizes=visible_controls_size(page,'#audit-filter input,#audit-filter select,#audit-filter button');assert sizes and all(v>=44 for v in sizes),sizes
        audit_geometry=page.evaluate('({inner:innerWidth,scroll:document.documentElement.scrollWidth})')
        assert audit_geometry['inner']<=width+1 and audit_geometry['scroll']<=width,audit_geometry
        # (6) 未知接口在探针页显示脱敏真实路径与实际结果，不再出现 [UNMATCHED]。
        unknown=page.request.get(base+'/Users/7/UnknownSecretFeature42');assert unknown.status==404
        page.evaluate('location.hash="probe"');page.wait_for_timeout(400)
        probe_text=page.locator('main').inner_text()
        assert '404 未实现' in probe_text,probe_text
        assert '[UNMATCHED]' not in probe_text
        assert '[redacted:' in probe_text,probe_text
        assert 'UnknownSecretFeature42' not in probe_text
        # (7) 运维诊断侧栏中的 IP/CIDR 黑名单：真实持久化、实时计数、拒绝误封自己。
        page.evaluate('location.hash="security"')
        net=page.locator('[data-security="network"]');expect(net).to_be_visible()
        net.fill('203.0.113.0/24');page.locator('[data-security="note"]').fill('报告回归测试');page.locator('[data-security="add"]').click()
        expect(page.locator('[data-network="203.0.113.0/24"]')).to_be_visible()
        rules=page.request.get(base+'/api/admin/security/ip-blocklist',headers={'X-Emby-Token':token}).json()
        assert any(i['network']=='203.0.113.0/24' and i['note']=='报告回归测试' and i['blocked']==0 for i in rules['items']),rules
        assert rules['max_rules']==256 and rules['client_ip']=='127.0.0.1' and rules['total_blocked']==0,rules
        net.fill('127.0.0.1');page.locator('[data-security="add"]').click()
        expect(page.locator('[data-security="error"]')).to_be_visible()
        loopback_error=page.locator('[data-security="error"]').inner_text()
        assert '误封' in loopback_error,loopback_error
        net.fill('not-an-ip');page.locator('[data-security="add"]').click()
        expect(page.locator('[data-security="error"]')).to_be_visible()
        assert page.request.get(base+'/api/admin/security/ip-blocklist',headers={'X-Emby-Token':token}).json()['items'][0]['network']=='203.0.113.0/24'
        geometry=page.evaluate('({inner:innerWidth,scroll:document.documentElement.scrollWidth,right:document.querySelector(".security-panel").getBoundingClientRect().right})')
        assert geometry['scroll']<=width and geometry['right']<=width+1,geometry
        if width<1000:
            sizes=visible_controls_size(page,'.security-panel input,.security-panel button');assert sizes and all(v>=44 for v in sizes),sizes
        page.screenshot(path=os.path.join(root,str(width)+'-security.png'),full_page=True)
        page.locator('[data-network="203.0.113.0/24"]').click()
        expect(page.locator('[data-network="203.0.113.0/24"]')).to_have_count(0)
        assert page.request.get(base+'/api/admin/security/ip-blocklist',headers={'X-Emby-Token':token}).json()['items']==[]
        # (2) 媒体库预览设置改为弹窗：打开后焦点进入、Esc 关闭、焦点回到触发按钮。
        page.evaluate('location.hash="libraries"');trigger=page.locator('[data-lib-sprites]').first;expect(trigger).to_be_visible()
        trigger.click();dialog=page.locator('#library-sprite-settings');expect(dialog).to_be_visible()
        assert page.evaluate('document.querySelector("#library-sprite-settings").contains(document.activeElement)')
        assert dialog.evaluate('(e)=>e.matches(":modal")') is True
        page.keyboard.press('Escape');expect(dialog).to_be_hidden()
        assert page.evaluate('document.activeElement&&document.activeElement.hasAttribute("data-lib-sprites")')
        assert page.evaluate('document.documentElement.scrollWidth')<=width
        assert page.locator('#library-sprite-settings').evaluate('(e)=>e.tagName')=='DIALOG'
        assert not errors,errors
        results.append({'viewport':[width,height],'select_option_readable':option,'scheduled_sprites_type':True,'scheduled_payload':created[-1],'log_newbar_removed':True,'log_pause_keeps_working':True,'activity_ip_result_filters':True,'probe_real_result':True,'blocklist_crud_and_self_lockout':True,'library_settings_modal':True,'page_errors':errors})
        ctx.close()
    ctx=browser.new_context(viewport={'width':375,'height':812},is_mobile=True,has_touch=True,reduced_motion='reduce');ctx.add_init_script('localStorage.setItem("emby_token","report-ordinary")');page=ctx.new_page();page.goto(base+'/admin#security',wait_until='networkidle')
    assert page.locator('.security-panel').count()==0
    assert page.request.get(base+'/api/admin/security/ip-blocklist',headers={'X-Emby-Token':'report-ordinary'}).status==403
    ctx.close();browser.close()
with open(os.path.join(root,'results.json'),'w') as f:json.dump({'real_app_auth_probe_activity_blocklist':True,'result':results},f,ensure_ascii=False,indent=2)
print(json.dumps({'ok':True,'evidence':root,'viewports':[[375,812],[768,1024],[1280,900]]}))
`
