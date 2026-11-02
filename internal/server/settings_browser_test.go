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
    {'id': 'wh_1', 'name': '家庭通知', 'enabled': False, 'url_configured': True,
     'events': ['item.rate'], 'format': 'json', 'timeout_seconds': 5, 'allow_private': False},
    {'id': 'wh_2', 'name': '播放通知', 'enabled': False, 'url_configured': True,
     'events': ['playback.stop'], 'format': 'form', 'timeout_seconds': 12, 'allow_private': False}
]}

with sync_playwright() as p:
    browser = p.chromium.launch(headless=True)
    for width, height in [(1440, 1000), (390, 844), (375, 812), (844, 390)]:
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
                if (options) {
                    const draft = JSON.parse(options.body);
                    fixture.targets = draft.targets.map((t, i) => ({...t, id: t.id || 'new_' + i,
                        url_configured: !!t.url || !t.clear_url}));
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
        # Real DOM checkbox toggles and the resulting PUT payload remain boolean.
        page.locator('[data-field="enabled"]').first.check()
        page.locator('[data-event="item.deleted"]').first.check()
        page.locator('#wh-save').click()
        assert page.evaluate('calls.length') == 1, 'unconfirmed subscription must not save'
        page.get_by_text('对新增的删除订阅，我已理解并确认上述风险。', exact=True).click()
        assert checkbox.is_checked()
        page.locator('#wh-save').click()
        assert page.evaluate('JSON.parse(calls[1].options.body).confirm_delete') is True
        assert not checkbox.is_checked()
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
    browser.close()
print('Browser screenshots: ' + str(root))
`
