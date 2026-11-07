package server

import (
	"os"
	"os/exec"
	"testing"
)

// Opt-in browser regression using only local assets and an in-memory API.
// No listener, live credentials, network requests or production writes are needed.
func TestWebhookSettingsBrowser(t *testing.T) {
	if os.Getenv("SETTINGS_BROWSER") != "1" {
		t.Skip("set SETTINGS_BROWSER=1; requires Python playwright and Chromium")
	}
	out, err := exec.Command("python3", "-c", webhookSettingsBrowserPython).CombinedOutput()
	t.Logf("%s", out)
	if err != nil {
		t.Fatal(err)
	}
}

const webhookSettingsBrowserPython = `
import copy, json, tempfile
from pathlib import Path
from playwright.sync_api import sync_playwright

root = Path(tempfile.mkdtemp(prefix='emby-settings-browser-', dir='/tmp'))
css = Path('web/style.css').read_text()
js = Path('web/webhook-settings.js').read_text()
fixture = {'version': 2, 'config_etag': '"fixture-1"', 'multi_target': True, 'targets': [
    {'id': 'wh_1', 'name': '家庭通知', 'enabled': False, 'url': 'https://example.invalid/hook?token=fixture', 'url_configured': True,
     'events': ['item.rate'], 'format': 'json', 'timeout_seconds': 5, 'allow_private': False},
    {'id': 'wh_2', 'name': '播放通知', 'enabled': False, 'url': 'https://example.invalid/hook?token=fixture', 'url_configured': True,
     'events': ['playback.stop'], 'format': 'form', 'timeout_seconds': 12, 'allow_private': False}
]}

with sync_playwright() as p:
    browser = p.chromium.launch(headless=True)
    for width, height in [(1440, 1000), (390, 844), (375, 812), (320, 568), (844, 390), (667, 280)]:
        page = browser.new_page(viewport={'width': width, 'height': height}, reduced_motion='reduce')
        errors, requests = [], []
        page.on('pageerror', lambda e: errors.append(str(e)))
        page.on('request', lambda r: requests.append(r.url))
        page.route('**/*', lambda route: route.abort())
        page.set_content('<meta name="viewport" content="width=device-width, initial-scale=1"><main class="main"><div class="content settings-category"><section id="webhook-settings" class="panel"></section></div></main>')
        page.add_style_tag(content=css)
        page.add_script_tag(content=js)
        page.evaluate('''async data => {
            window.fixture = structuredClone(data); window.calls = [];
            window.mockAPI = async (path, options) => {
                calls.push({path, options});
                if (path.startsWith('/webhook/deliveries')) {
                    return options ? {cleared: 0} : {items: [
                        {id: 2, event: 'item.updated', item_id: '43', target_id: 'wh_2', status: 'failed', error_code: 'timeout', created_at: '2026-11-07T00:00:00Z'},
                        {id: 1, event: 'item.updated', item_id: '43', target_id: 'wh_1', status: 'sent', error_code: '', created_at: '2026-11-07T00:00:00Z'}
                    ], retention: {max_rows: 1000, days: 7}};
                }
                if (options) {
                    const draft = JSON.parse(options.body);
                    const previous = fixture.targets;
                    fixture.targets = draft.targets.map((t, i) => {
                        const old = previous.find(item => item.id === t.id);
                        const url = t.clear_url ? '' : (t.url || old?.url || '');
                        return {...t, id: t.id || 'new_' + i, url, url_configured: !!url};
                    });
                    fixture.config_etag = '"fixture-2"';
                }
                return structuredClone(fixture);
            };
            await mountWebhookSettings(document.querySelector('#webhook-settings'), mockAPI);
        }''', fixture)
        def check_hidden(selector, hidden):
            node = page.locator(selector)
            assert node.evaluate('(el) => el.hidden') == hidden, selector
            assert node.is_visible() != hidden, selector
            if hidden:
                assert node.evaluate('(el) => getComputedStyle(el).display') == 'none', selector
        page.wait_for_function('document.querySelectorAll("#wh-history-body tbody tr").length === 2')
        assert '失败' in page.locator('#wh-history-body tbody tr').nth(0).inner_text()
        assert '已确认' in page.locator('#wh-history-body tbody tr').nth(1).inner_text()
        for selector in ['#wh-remove-preview', '#wh-confirm-remove-row', '#wh-confirm-empty-row']:
            check_hidden(selector, True)
        assert page.locator('#wh-confirm-delete').get_attribute('type') == 'checkbox'
        boxes = page.locator('.webhook-target')
        summary_height = boxes.first.locator('summary').bounding_box()['height']
        card_height = boxes.first.bounding_box()['height']
        assert summary_height >= 44 and card_height < 90, (summary_height, card_height)
        page.screenshot(path=str(root / ('collapsed-%s.png' % width)), full_page=True)
        for summary in page.locator('.webhook-target > summary').all():
            summary.click()
        assert page.evaluate('''() => {
            const ids = [...document.querySelectorAll('[id]')].map(el => el.id);
            return new Set(ids).size === ids.length && [...document.querySelectorAll('.webhook-target input, .webhook-target select')].every(el => el.labels.length > 0);
        }''')
        page.get_by_label('超时（秒，1–30）', exact=True).first.click()
        assert page.get_by_label('超时（秒，1–30）', exact=True).first.evaluate('(el) => document.activeElement === el')
        page.get_by_label('编码格式', exact=True).first.select_option('form')
        assert page.get_by_label('编码格式', exact=True).first.input_value() == 'form'
        checkbox = page.locator('#wh-confirm-delete')
        assert checkbox.bounding_box()['width'] == 16 and checkbox.bounding_box()['height'] == 16
        assert checkbox.locator('..').bounding_box()['height'] >= 44
        assert page.evaluate('document.documentElement.scrollWidth <= innerWidth')
        page.screenshot(path=str(root / ('expanded-%s.png' % width)), full_page=True)
        # The modal edits an isolated draft: cancel, close and native Escape discard it.
        original_count = boxes.count()
        def open_draft():
            page.locator('#wh-add').click()
            assert page.locator('#wh-dialog').evaluate('(el) => el.open')
            assert page.locator('#wh-draft-name').evaluate('(el) => document.activeElement === el')
            assert boxes.count() == original_count
        def fill_draft():
            page.locator('#wh-draft-name').fill('本地草稿')
            page.locator('#wh-draft-url').fill('https://example.invalid/new?token=plain-fixture')
            page.locator('#wh-dialog [data-event="item.updated"]').check()
        for action in ['#wh-dialog-cancel', '#wh-dialog-close', 'Escape']:
            open_draft()
            fill_draft()
            if action == 'Escape':
                page.keyboard.press('Escape')
            else:
                page.locator(action).click()
            page.wait_for_function('!document.querySelector("#wh-dialog").open && !document.querySelector("#wh-dialog-body").children.length')
            assert boxes.count() == original_count
            assert page.locator('#wh-add').evaluate('(el) => document.activeElement === el')
            assert page.evaluate('calls.length') == 2
        open_draft()
        assert page.locator('#wh-draft-name').input_value() == ''
        assert page.locator('#wh-draft-url').get_attribute('type') == 'text'
        assert page.locator('#wh-dialog button[type="submit"]').inner_text() == '添加到草稿'
        # Both keyboard directions wrap inside the native modal.
        page.locator('#wh-dialog-close').focus()
        page.keyboard.press('Shift+Tab')
        assert page.locator('#wh-dialog button[type="submit"]').evaluate('(el) => document.activeElement === el'), page.evaluate('document.activeElement.outerHTML')
        page.keyboard.press('Tab')
        assert page.locator('#wh-dialog-close').evaluate('(el) => document.activeElement === el'), page.evaluate('document.activeElement.outerHTML')
        assert page.evaluate('''() => {
            const ids = [...document.querySelectorAll('[id]')].map(el => el.id);
            return new Set(ids).size === ids.length && [...document.querySelectorAll('#wh-dialog input, #wh-dialog select')].every(el => el.labels.length);
        }''')
        dialog_rect = page.locator('#wh-dialog').bounding_box()
        assert dialog_rect['x'] >= 0 and dialog_rect['y'] >= 0, dialog_rect
        assert dialog_rect['x'] + dialog_rect['width'] <= width + 1, dialog_rect
        assert dialog_rect['y'] + dialog_rect['height'] <= height + 1, dialog_rect
        assert page.locator('#wh-dialog').evaluate('(el) => el.scrollWidth <= el.clientWidth')
        page.screenshot(path=str(root / ('modal-%s-%s.png' % (width, height))))
        submit = page.locator('#wh-dialog button[type="submit"]')
        submit.click()
        assert page.locator('#wh-dialog-status').inner_text()
        page.locator('#wh-draft-name').fill('本地草稿')
        submit.click()  # Events are mandatory.
        assert '事件' in page.locator('#wh-dialog-status').inner_text()
        page.locator('#wh-dialog [data-event="item.updated"]').check()
        for invalid in ['', 'not a URL', 'ftp://example.invalid/hook', 'javascript:alert(1)']:
            page.locator('#wh-draft-url').fill(invalid)
            submit.click()
            assert page.locator('#wh-dialog').evaluate('(el) => el.open')
            assert 'HTTP(S)' in page.locator('#wh-dialog-status').inner_text()
            assert boxes.count() == original_count
            assert page.evaluate('calls.length') == 2
        fill_draft()
        page.locator('#wh-draft-format').select_option('form')
        page.locator('#wh-draft-timeout').fill('17')
        page.locator('#wh-draft-allow-private').check()
        submit.click()
        page.wait_for_function('!document.querySelector("#wh-dialog").open')
        assert boxes.count() == original_count + 1
        assert page.evaluate('calls.length') == 2, 'adding a draft must not call API'
        # Real DOM checkbox toggles and the resulting PUT payload remain boolean.
        page.locator('[data-field="enabled"]').first.check()
        page.locator('[data-event="item.deleted"]').first.check()
        page.locator('#wh-save').click()
        assert page.evaluate('calls.length') == 2, 'unconfirmed subscription must not save'
        page.get_by_text('对新增的删除订阅，我已理解并确认上述风险。', exact=True).click()
        assert checkbox.is_checked()
        page.locator('#wh-save').click()
        assert page.evaluate('JSON.parse(calls[2].options.body).confirm_delete') is True
        assert not checkbox.is_checked()
        saved = page.evaluate('JSON.parse(calls[2].options.body)')
        assert saved['targets'][2]['url'] == 'https://example.invalid/new?token=plain-fixture'
        assert saved['targets'][2]['format'] == 'form'
        assert saved['targets'][2]['timeout_seconds'] == 17
        assert saved['targets'][2]['allow_private'] is True
        page.on('dialog', lambda dialog: dialog.accept())
        page.locator('#wh-reload').click()
        page.wait_for_function('calls.length === 4 && !document.querySelector("#wh-save").disabled')
        for card in boxes.all():
            card.locator('summary').click()
            address = card.locator('[data-field="url"]')
            assert address.is_visible() and address.get_attribute('type') == 'text'
            assert address.input_value().startswith('https://example.invalid/')
        # Blank preserves, a replacement changes, and explicit clear removes stored URLs.
        boxes.nth(0).locator('[data-field="url"]').fill('')
        boxes.nth(1).locator('[data-field="url"]').fill('http://example.invalid/replaced')
        boxes.nth(2).locator('[data-field="clear-url"]').check()
        page.locator('#wh-save').click()
        page.wait_for_function('calls.length === 5 && !document.querySelector("#wh-save").disabled')
        changed = page.evaluate('JSON.parse(calls[4].options.body).targets')
        assert 'url' not in changed[0], 'blank URL must preserve the stored address'
        assert changed[1]['url'] == 'http://example.invalid/replaced'
        assert changed[2]['clear_url'] is True and 'url' not in changed[2]
        assert page.evaluate('fixture.targets.map(t => t.url)') == ['https://example.invalid/hook?token=fixture', 'http://example.invalid/replaced', '']
        # Remove the third saved target first, then preserve the original removal regressions.
        boxes.nth(2).locator('summary').click()
        boxes.nth(2).locator('[data-remove]').click()
        # Saved cards collapse; removing existing and new targets drives all hidden states.
        page.locator('.webhook-target > summary').first.click()
        page.locator('[data-remove]').first.click()
        check_hidden('#wh-remove-preview', False)
        check_hidden('#wh-confirm-remove-row', False)
        check_hidden('#wh-confirm-empty-row', True)
        page.locator('.webhook-target > summary').first.click()
        page.locator('[data-remove]').first.click()
        check_hidden('#wh-confirm-empty-row', False)
        page.locator('#wh-add').click()
        check_hidden('#wh-confirm-empty-row', False)
        fill_draft()
        page.locator('#wh-dialog button[type="submit"]').click()
        page.wait_for_function('!document.querySelector("#wh-dialog").open')
        check_hidden('#wh-confirm-empty-row', True)
        check_hidden('#wh-confirm-remove-row', False)
        page.locator('[data-remove]').first.click()
        check_hidden('#wh-confirm-empty-row', False)
        page.locator('#wh-confirm-remove').check()
        page.locator('#wh-confirm-empty').check()
        page.locator('#wh-save').click()
        check_hidden('#wh-remove-preview', True)
        check_hidden('#wh-confirm-remove-row', True)
        check_hidden('#wh-confirm-empty-row', False)
        page.locator('#wh-add').click()
        check_hidden('#wh-confirm-empty-row', False)
        fill_draft()
        page.locator('#wh-dialog button[type="submit"]').click()
        page.wait_for_function('!document.querySelector("#wh-dialog").open')
        check_hidden('#wh-confirm-empty-row', True)
        assert not page.locator('#wh-confirm-empty').is_checked()
        page.locator('[data-remove]').click()
        check_hidden('#wh-remove-preview', True)
        check_hidden('#wh-confirm-remove-row', True)
        check_hidden('#wh-confirm-empty-row', False)
        assert not errors, errors
        assert not requests, requests
        print(json.dumps({'viewport': [width, height], 'collapsed_height': card_height,
                          'summary_height': summary_height, 'network_requests': len(requests), 'result': 'PASS'}))
        page.close()
    # Independent explicit-test regressions preserve every save/modal assertion above.
    for width in [1440, 390, 320]:
        page = browser.new_page(viewport={'width': width, 'height': 844})
        errors, requests = [], []
        page.on('pageerror', lambda e: errors.append(str(e)))
        page.on('request', lambda r: requests.append(r.url))
        page.route('**/*', lambda route: route.abort())
        page.set_content('<meta name="viewport" content="width=device-width, initial-scale=1"><main class="main"><section id="webhook-settings" class="panel"></section></main>')
        page.add_style_tag(content=css)
        page.add_script_tag(content=js)
        page.evaluate('''async data => {
            window.fixture = structuredClone(data); window.calls = []; window.current = true;
            window.mockAPI = async (path, options) => {
                calls.push({path, options});
                if (path.startsWith('/webhook/deliveries')) {
                    return options ? {cleared: 0} : {items: [], retention: {max_rows: 1000, days: 7}};
                }
                if (path === '/webhook/test') {
                    window.testSignal = options.signal;
                    if (window.testDelay) await new Promise(resolve => window.resolveTest = resolve);
                    return window.testResult || {status:'sent', duration_ms:37, error_code:'', message:'测试已发送'};
                }
                if (options) throw Error('test action must not persist configuration');
                return structuredClone(fixture);
            };
            await mountWebhookSettings(document.querySelector('#webhook-settings'), mockAPI, () => current);
        }''', fixture)
        assert page.evaluate('calls.length') == 2, 'mount must not send a test'
        card = page.locator('[data-target-index]').first
        card.locator('summary').click()
        assert card.locator('[data-test]').get_attribute('type') == 'button'
        assert card.locator('[data-test-status]').get_attribute('role') in ['status', 'alert']
        card.locator('[data-field="name"]').fill('')
        for event in card.locator('[data-event]').all():
            event.uncheck()
        for encoding in ['json', 'form']:
            card.locator('[data-field="url"]').fill('' if encoding == 'form' else 'https://example.invalid/current')
            card.locator('[data-field="format"]').select_option(encoding)
            card.locator('[data-field="timeout"]').fill('17')
            card.locator('[data-field="allow-private"]').check()
            card.locator('[data-test]').click()
            page.wait_for_function('!document.querySelector("[data-target-index] [data-test]").disabled')
            sent = page.evaluate('calls[calls.length-1]')
            assert sent['path'] == '/webhook/test' and sent['options']['method'] == 'POST'
            assert json.loads(sent['options']['body']) == {'url': 'https://example.invalid/hook?token=fixture' if encoding == 'form' else 'https://example.invalid/current', 'format': encoding, 'timeout_seconds': 17, 'allow_private': True}
            assert '37' in card.locator('[data-test-status]').inner_text()
        count = page.evaluate('calls.length')
        card.locator('[data-field="clear-url"]').check()
        card.locator('[data-test]').click()
        assert page.evaluate('calls.length') == count, 'explicit clear must not fall back'
        assert card.locator('[data-test-status]').inner_text()
        card.locator('[data-field="clear-url"]').uncheck()
        card.locator('[data-field="url"]').fill('https://example.invalid/failure')
        page.evaluate("testResult = {status:'failed',duration_ms:81,error_code:'delivery_failed',message:'接收失败'}")
        card.locator('[data-test]').click()
        page.wait_for_function('!document.querySelector("[data-target-index] [data-test]").disabled')
        assert '81' in card.locator('[data-test-status]').inner_text()
        assert card.locator('[data-test-status]').get_attribute('role') == 'alert'
        page.locator('#wh-add').click()
        button = page.locator('#wh-dialog [data-test]')
        assert button.get_attribute('type') == 'button'
        for invalid in ['', 'not a URL', 'ftp://example.invalid']:
            page.locator('#wh-draft-url').fill(invalid)
            count = page.evaluate('calls.length')
            button.click()
            assert page.evaluate('calls.length') == count
            assert page.locator('#wh-dialog [data-test-status]').inner_text()
        page.locator('#wh-draft-url').fill('https://example.invalid/modal')
        page.locator('#wh-draft-format').select_option('form')
        page.locator('#wh-draft-timeout').fill('13')
        page.locator('#wh-draft-allow-private').check()
        page.evaluate('testDelay = true')
        button.click()
        page.evaluate('document.querySelector("#wh-dialog [data-test]").click()')
        assert button.is_disabled()
        assert page.evaluate('calls.length') == count + 1, 'duplicate test click must not send twice'
        for selector in ['#wh-save', '#wh-reload', '#wh-add', '#wh-dialog button[type="submit"]']:
            assert page.locator(selector).is_disabled(), selector
        assert page.locator('#wh-dialog-body input, #wh-dialog-body select').evaluate_all('(nodes) => nodes.every(n => n.disabled)')
        assert not page.locator('#wh-dialog-close').is_disabled()
        assert json.loads(page.evaluate('calls[calls.length-1].options.body')) == {'url':'https://example.invalid/modal', 'format':'form', 'timeout_seconds':13, 'allow_private':True}
        assert page.locator('#wh-dialog').evaluate('(el) => el.scrollWidth <= el.clientWidth')
        assert page.evaluate('document.documentElement.scrollWidth <= innerWidth')
        count = page.evaluate('calls.length')
        if width == 390:
            page.locator('#wh-dialog-close').click()
        else:
            page.keyboard.press('Escape')
        page.wait_for_function('!document.querySelector("#wh-dialog").open && !document.querySelector("#wh-add").disabled')
        assert page.evaluate('testSignal.aborted')
        page.locator('#wh-add').click()
        page.locator('#wh-draft-url').fill('https://example.invalid/new-modal')
        clean = page.locator('#wh-dialog [data-test-status]').inner_text()
        page.evaluate('''async () => { resolveTest(); await new Promise(r => setTimeout(r, 30)); }''')
        assert page.locator('#wh-dialog [data-test-status]').inner_text() == clean
        assert page.locator('#wh-draft-url').input_value() == 'https://example.invalid/new-modal'
        assert page.evaluate('calls.length') == count, 'no retry or implicit save'
        assert page.evaluate('fixture.targets[0].name') == fixture['targets'][0]['name']
        assert not errors, errors
        assert not requests, requests
        print(json.dumps({'test_action_viewport': width, 'result': 'PASS', 'network_requests': len(requests)}))
        page.close()
    browser.close()
print('Browser screenshots: ' + str(root))
`
