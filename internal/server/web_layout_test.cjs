'use strict';
// Real Chromium + the complete frontend, with local fixture APIs only. No business server.
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const os = require('node:os');
const { spawn } = require('node:child_process');
const { pathToFileURL } = require('node:url');
const chrome = process.env.CHROME_BIN || '/root/.cache/ms-playwright/chromium-1248/chrome-linux64/chrome';
const web = path.join(__dirname, 'web');

async function browser(t) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'emby-layout-'));
  const proc = spawn(chrome, ['--headless', '--no-sandbox', '--disable-gpu', '--no-first-run', '--remote-debugging-port=0', '--user-data-dir=' + path.join(dir, 'profile'), 'about:blank']);
  const exited = new Promise(resolve => { proc.once('exit', resolve); proc.once('error', resolve); });
  let ws;
  t.after(async () => {
    // Prefer Chromium's own shutdown, so renderers finish profile writes too.
    if (ws && ws.readyState === WebSocket.OPEN) {
      try { ws.send(JSON.stringify({ id: 0, method: 'Browser.close' })); } catch { /* fallback below */ }
    }
    const terminate = setTimeout(() => proc.kill(), 1000);
    const forceKill = setTimeout(() => proc.kill('SIGKILL'), 2500);
    try { await exited; } finally { clearTimeout(terminate); clearTimeout(forceKill); if (ws) ws.close(); }
    assert.equal(path.dirname(dir), os.tmpdir());
    assert.ok(path.basename(dir).startsWith('emby-layout-'));
    // Chromium descendants may finish profile writes just after the browser
    // process exits. Retry only transient filesystem errors, with a bounded
    // backoff; never suppress a persistent cleanup failure.
    fs.rmSync(dir, { recursive: true, force: true, maxRetries: 8, retryDelay: 100 });
  });
  const url = await new Promise((resolve, reject) => {
    let text = '';
    const timeout = setTimeout(() => reject(Error('Chromium CDP startup timeout: ' + text)), 15000);
    proc.once('error', e => { clearTimeout(timeout); reject(e); });
    proc.stderr.on('data', chunk => { text += chunk; const match = text.match(/DevTools listening on (ws:\/\/[^\s]+)/); if (match) { clearTimeout(timeout); resolve(match[1]); } });
    proc.once('exit', code => { clearTimeout(timeout); reject(Error('Chromium exited ' + code + ': ' + text)); });
  });
  ws = new WebSocket(url);
  await new Promise((resolve, reject) => { ws.addEventListener('open', resolve, { once: true }); ws.addEventListener('error', reject, { once: true }); });
  let serial = 0;
  const pending = new Map();
  ws.addEventListener('message', event => {
    const message = JSON.parse(event.data);
    const entry = pending.get(message.id);
    if (!entry) return;
    pending.delete(message.id); clearTimeout(entry.timeout);
    if (message.error) entry.reject(Error(JSON.stringify(message.error))); else entry.resolve(message.result);
  });
  const send = (method, params = {}, sessionId) => new Promise((resolve, reject) => {
    const id = ++serial;
    const timeout = setTimeout(() => { pending.delete(id); reject(Error('CDP timeout ' + method)); }, 15000);
    pending.set(id, { resolve, reject, timeout });
    ws.send(JSON.stringify({ id, method, params, ...(sessionId ? { sessionId } : {}) }));
  });
  const { targetId } = await send('Target.createTarget', { url: 'about:blank' });
  const { sessionId } = await send('Target.attachToTarget', { targetId, flatten: true });
  const call = (method, params) => send(method, params, sessionId);
  await call('Page.enable');
  const evaluate = async expression => {
    const result = await call('Runtime.evaluate', { expression, awaitPromise: true, returnByValue: true });
    if (result.exceptionDetails) throw Error(JSON.stringify(result.exceptionDetails));
    return result.result.value;
  };
  const fixture = `
    window.__calls = []; window.__errors = [];
    addEventListener('error', e => __errors.push(e.message));
    localStorage.setItem('emby_token','layout-fixture');
    window.confirm=()=>true;
    window.fetch=async (input, options={})=>{
      const url=String(input), p=url.split('?')[0]; __calls.push({url,method:options.method||'GET'});
      let data={};
      if(p==='/Users/Me')data={Name:'owner',Policy:{IsAdministrator:true}};
      else if(p.endsWith('/libraries'))data={total:1,items:[{Id:1,Name:'测试媒体库',Path:'/media/'+ 'long-path/'.repeat(12),count:1}]};
      else if(p.endsWith('/items'))data={total:2,items:[{id:1,status:'success',Title:'示例影片：移动端资料管理',Number:'TEST-001',source_path:'/media/测试/TEST-001.strm'},{id:2,status:'pending',Title:'等待处理的影片',source_path:'/media/待处理/TEST-002.strm'}]};
      else if(p.endsWith('/detail')){
        if(window.__detailDelay)await new Promise(r=>window.__resolveDetail=r);
        if(window.__detailFail)return {ok:false,status:503,json:async()=>({error:'详情暂不可用，请重试'})};
        data={movie:{id:1,Title:'长详情验证',Status:'success',Plot:'长简介与文件路径 '.repeat(800)},files:[]};
      }
      else if(p.endsWith('/settings'))data={listen:':18080',cache:'memory',db_path:'/data/emby.db',session_ttl_days:7};
      else if(p.endsWith('/users'))data={users:[{id:1,name:'owner',is_admin:true,library_ids:[1]}]};
      else if(p.endsWith('/apikeys'))data={total:1,items:[{id:'stable',name:'本地脚本',key_prefix:'prefix',version:1}]};
      else if(p.endsWith('/recovery'))data={items:[]};
      else if(p.endsWith('/webhook/targets'))data={config_etag:'fixture',targets:[{id:'one',name:'通知目标',enabled:true,url_configured:true,events:['item.updated'],format:'json',timeout_seconds:5,allow_private:false}]};
      else if(p.endsWith('/scheduled'))data={items:[{id:1,name:'夜间扫描',type:'scan',cron:'0 3 * * *',params:'{}',enabled:true,config_etag:'fixture'}]};
      else if(p.endsWith('/dashboard')){
        // Deliberately synthetic layout fixture; real HTTP telemetry is verified by Go/CDP integration.
        const minutes=Number(new URL(url,'http://fixture').searchParams.get('window')?.replace('m','')||15), now=Date.now();
        data={instance_id:'layout-fixture',started_at:new Date(now-120000).toISOString(),generated_at:new Date(now).toISOString(),retention_minutes:60,window_minutes:minutes,window_start:new Date(now-minutes*60000).toISOString(),window_end:new Date(now).toISOString(),requests:{total:60,errors_4xx:2,errors_5xx:1,rate_per_second:.5,p50_ms:2,p95_ms:20,p50_overflow:false,p95_overflow:false,sampled_seconds:120},latency:{unit:'ms',method:'histogram_upper_bound',max_finite_ms:60000,scope:'json_control_plane'},runtime:{uptime_seconds:120,goroutines:24,heap_alloc_bytes:8388608,heap_inuse_bytes:10485760,go_sys_bytes:16777216,num_gc:3},media:{available:true,stale:false,success:36,manual:4,pending:2,total:42,libraries:1,updated_at:new Date(now).toISOString()},tasks:{running:0,recent:[{id:1,kind:'scan',status:'success',started_at:new Date(now-10000).toISOString(),ended_at:new Date(now-5000).toISOString()}]},series:Array.from({length:minutes},(_,i)=>({start:new Date(now-(minutes-i-1)*60000).toISOString(),observed:i>=minutes-2,requests:i>=minutes-2?30:null,errors_4xx:i>=minutes-2?1:null,errors_5xx:i>=minutes-2?0:null,p50_ms:i>=minutes-2?2:null,p95_ms:i>=minutes-2?20:null,p50_overflow:false,p95_overflow:false}))};
      }
      else if(p.endsWith('/tasks'))data={items:[]};
      else if(p.endsWith('/logs'))data={items:[]};
      else if(p.endsWith('/scrape/settings'))data={};
      else if(p.endsWith('/status'))data={running:false};
      else if(p.endsWith('/log/settings'))data={retention_days:7,config_etag:'fixture'};
      return {ok:true,status:200,headers:{get:()=>null},json:async()=>data};
    };`;
  await call('Page.addScriptToEvaluateOnNewDocument', { source: fixture });
  let html = fs.readFileSync(path.join(web, 'index.html'), 'utf8');
  html = html.replace(/<link[^>]*href="\/web\/([^\"]+\.css)"[^>]*>/g, (_, name) => '<style>' + fs.readFileSync(path.join(web, name), 'utf8') + '</style>');
  html = html.replace(/<script[^>]*src="\/web\/([^\"]+)"[^>]*><\/script>/g, (_, name) => {
    if (name.startsWith('vendor/')) return '';
    return '<script>' + fs.readFileSync(path.join(web, name), 'utf8').replace(/<\/script/gi, '<\\/script') + '</script>';
  });
  const file = path.join(dir, 'index.html'); fs.writeFileSync(file, html);
  await call('Page.navigate', { url: pathToFileURL(file).href });
  await evaluate(`(async()=>{for(let i=0;i<100;i++){if(document.querySelector('#overview-dashboard'))return;await new Promise(r=>setTimeout(r,20));}throw Error('boot did not render: '+document.body.innerText);})()`);
  return { call, evaluate, dir };
}

test('完整前端：桌面与移动端页面布局、渐进表单和触控导航', { timeout: 120000 }, async t => {
  if (!fs.existsSync(chrome) || typeof WebSocket !== 'function') { t.skip('需要现有Chromium和Node WebSocket；不自动安装'); return; }
  const { call, evaluate } = await browser(t);
  await call('Emulation.setEmulatedMedia', { features: [{ name:'prefers-reduced-motion', value:'reduce' }] });
  for (const width of [1440, 768, 390, 360, 320]) {
    await call('Emulation.setDeviceMetricsOverride', { width, height: 844, deviceScaleFactor: 1, mobile: width < 860 });
    for (const name of ['overview', 'libraries', 'settings', 'scrape', 'translate', 'scheduled', 'items']) {
      const actual = await evaluate(`(async()=>{await page('${name}');await new Promise(r=>setTimeout(r,250));return {width:innerWidth,scroll:document.documentElement.scrollWidth,error:document.querySelector('#content').innerText,errors:__errors.slice()};})()`);
      assert.equal(actual.width, width, name);
      assert.ok(actual.scroll <= width + 1, `${name} at ${width}: scrollWidth=${actual.scroll}`);
      assert.deepEqual(actual.errors, [], name);
      assert.doesNotMatch(actual.error, /加载失败|请求失败|TypeError|ReferenceError/);
      if (name === 'overview') {
        const metrics = await evaluate(`(()=>{const r=document.querySelector('#overview-dashboard');return {root:!!r,live:r?.getAttribute('aria-live'),sample:document.querySelector('[data-dash="p95"]')?.textContent,charts:r?.querySelectorAll('svg[role="img"]').length,axes:[...r.querySelectorAll('.dash-axis')].map(n=>parseFloat(getComputedStyle(n).fontSize)*n.getScreenCTM().a),controls:[...r.querySelectorAll('.dash-toolbar button')].filter(b=>b.getClientRects().length).map(b=>({h:b.getBoundingClientRect().height}))};})()`);
        assert.equal(metrics.root,true); assert.equal(metrics.live,'off'); assert.equal(metrics.sample,'20'); assert.equal(metrics.charts,2); assert.ok(metrics.axes.every(size=>size>=11.99),'real CSS axis size '+width+': '+metrics.axes);
        assert.ok(metrics.controls.every(b=>b.h>=43.99),`dashboard touch controls at ${width}`);
        const switched=await evaluate(`(async()=>{document.querySelector('[data-window="5"]').click();document.querySelector('[data-window="60"]').click();await new Promise(r=>setTimeout(r,40));return {selected:document.querySelector('[data-window="60"]').getAttribute('aria-pressed'),rows:document.querySelectorAll('[data-dash="samples"] tr').length,scroll:document.documentElement.scrollWidth};})()`);
        assert.equal(switched.selected,'true');assert.equal(switched.rows,60);assert.ok(switched.scroll<=width+1);
        const pause=await evaluate(`(()=>{const b=document.querySelector('[data-dash="pause"]');b.click();return {pressed:b.getAttribute('aria-pressed'),state:document.querySelector('[data-dash="state"]').textContent};})()`);
        assert.equal(pause.pressed,'true');assert.match(pause.state,/暂停/);
        await evaluate(`document.querySelector('[data-dash="pause"]').click()`);
      }
      if (name === 'settings') {
        const state = await evaluate(`(()=>{const buttons=[...document.querySelectorAll('.settings-nav button')];return {count:buttons.length,shown:[...document.querySelectorAll('.settings-category')].filter(e=>!e.hidden).length};})()`);
        assert.ok(state.count >= 4, 'settings categories available');
        assert.equal(state.shown, 1, 'one settings category at a time');
        const categories = await evaluate(`[...document.querySelectorAll('[data-settings-category]')].map(b=>b.dataset.settingsCategory)`);
        for (const category of categories) {
          const result = await evaluate(`(()=>{document.querySelector('[data-settings-category="${category}"]').click();return {shown:[...document.querySelectorAll('.settings-category')].filter(e=>!e.hidden).length,scroll:document.documentElement.scrollWidth,selected:document.querySelector('[data-settings-category="${category}"]').getAttribute('aria-pressed')};})()`);
          assert.equal(result.shown, 1, category); assert.equal(result.selected, 'true');
          assert.ok(result.scroll <= width + 1, `settings ${category} at ${width}: ${result.scroll}`);
        }
        const webhook = await evaluate(`(()=>{document.querySelector('[data-settings-category="notifications"]').click();const d=document.querySelector('[data-target-index]');if(!d)return {missing:true};const before=d.open;d.querySelector('summary').click();return {before,after:d.open,field:!!d.querySelector('[data-field="url"]')};})()`);
        assert.equal(webhook.missing, undefined); assert.equal(webhook.before, false); assert.equal(webhook.after, true); assert.equal(webhook.field, true);
      }
      if (name === 'scheduled') {
        const state = await evaluate(`(()=>{const form=document.querySelector('#sched-form'),details=form.closest('details');return {collapsed:!!details&&!details.open,first:document.querySelector('#content').innerText.indexOf('已配置任务')<document.querySelector('#content').innerText.indexOf('新建计划任务')};})()`);
        assert.equal(state.collapsed, true); assert.equal(state.first, true);
        const editing = await evaluate(`(async()=>{const b=document.querySelector('[data-role="edit"]');b.click();await new Promise(r=>setTimeout(r,50));const focused=document.activeElement.id;document.querySelector('#sched-cancel').click();await new Promise(r=>setTimeout(r,50));const r=document.querySelector('[data-role="run"]').getBoundingClientRect();return {focused,restored:document.activeElement.dataset.role,x:r.x,right:r.right};})()`);
        assert.equal(editing.focused,'s-name'); assert.equal(editing.restored,'edit');
        if(width<=650){assert.ok(editing.x>=0&&editing.right<=width,'primary action visible without horizontal scrolling');}
      }
      if (name === 'scrape') {
        assert.equal(await evaluate(`!!document.querySelector('#scrape-form')?.closest('details')&&!document.querySelector('#scrape-form').closest('details').open`), true);
      }
      const expanded = await evaluate(`(async()=>{const nodes=[...document.querySelectorAll('#content details')];nodes.forEach(d=>d.open=true);await new Promise(r=>setTimeout(r,20));const scroll=document.documentElement.scrollWidth;nodes.forEach(d=>d.open=false);return scroll;})()`);
      assert.ok(expanded <= width + 1, `expanded ${name} at ${width}: ${expanded}`);
      if (name === 'items') {
        await evaluate(`document.querySelector('.wall-maintenance').open=true;document.querySelector('#wall-translate-toggle').click()`);
        const hits = await evaluate(`(()=>{return [...document.querySelectorAll('.wall-card:first-child [data-probe],.wall-card:first-child [data-reread],.wall-card:first-child [data-delete],.wall-card:first-child [data-translate-select]')].map(b=>{b.scrollIntoView({block:'center'});const target=b.matches('input')?b.closest('label'):b;target.scrollIntoView({block:'center'});const r=target.getBoundingClientRect(),hit=document.elementFromPoint(r.x+r.width/2,r.y+r.height/2);return {ok:hit===target||target.contains(hit),width:r.width,height:r.height,button:b.tagName==='BUTTON'};});})()`);
        assert.equal(hits.length,4);if(width<=860)assert.ok(hits.every(h=>h.ok),`card hit targets at ${width}: ${JSON.stringify(hits)}`);
        if(width<=860)assert.ok(hits.filter(h=>h.button).every(h=>h.width>=43.99&&h.height>=43.99),`card touch dimensions at ${width}`);
        const detail = await evaluate(`(async()=>{await openDetail({id:1});const maintenance=document.querySelector('.detail-maintenance');if(!maintenance)return {missing:true};const collapsed=!maintenance.open;maintenance.open=true;document.querySelector('#detail-edit-toggle').click();await new Promise(r=>setTimeout(r,20));const result={collapsed,scroll:document.documentElement.scrollWidth,shown:!document.querySelector('#detail-edit').hidden};document.querySelector('#detail-close').click();return result;})()`);
        assert.equal(detail.missing, undefined);assert.equal(detail.collapsed,true);assert.equal(detail.shown,true);assert.ok(detail.scroll<=width+1,`detail editor at ${width}: ${detail.scroll}`);
      }
      if (process.env.EMBY_LAYOUT_ARTIFACT_DIR && [1280,390].includes(width) && ['overview','settings','scheduled','items'].includes(name)) {
        const output = path.resolve(process.env.EMBY_LAYOUT_ARTIFACT_DIR);
        fs.mkdirSync(output, { recursive: true });
        const shot = await call('Page.captureScreenshot', { format:'png' });
        fs.writeFileSync(path.join(output, `${name}-${width}.png`), Buffer.from(shot.data, 'base64'));
      }
    }
    if (width < 860) {
      const result = await evaluate(`(()=>{const b=document.querySelector('[aria-controls="nav-panel"]')||document.querySelector('#nav-toggle');if(!b)return {missing:true};b.focus();b.click();const opened=b.getAttribute('aria-expanded')==='true';document.dispatchEvent(new KeyboardEvent('keydown',{key:'Escape',bubbles:true}));return {opened,closed:b.getAttribute('aria-expanded')==='false',restored:document.activeElement===b,height:b.getBoundingClientRect().height};})()`);
      assert.equal(result.missing, undefined); assert.equal(result.opened, true); assert.equal(result.closed, true); assert.equal(result.restored, true); assert.ok(result.height >= 44);
    }
  }
  assert.equal(await evaluate(`__calls.filter(c=>c.method!=='GET'&&!c.url.endsWith('/scheduled/validate')).length`), 0, 'layout operations never submit business mutations');
});


test('详情固定外壳：加载关闭、错误重试、长详情关闭、背景隔离、持久反馈与焦点归还', {timeout:60000}, async t => {
  if (!fs.existsSync(chrome) || typeof WebSocket !== 'function') { t.skip('需要现有Chromium'); return; }
  const {call,evaluate}=await browser(t);
  await call('Emulation.setDeviceMetricsOverride',{width:320,height:740,deviceScaleFactor:1,mobile:true});
  await evaluate("page('items')");
  const loading=await evaluate(`(async()=>{const trigger=document.querySelector('.wall-title');trigger.focus();window.__detailDelay=true;void openDetail({id:1});const result={close:!!document.querySelector('#detail-close'),focus:document.activeElement.id,inert:document.querySelector('#app-shell').inert,lock:document.body.style.overflow};closeDetail();result.restored=document.activeElement===trigger;result.unlocked=document.body.style.overflow;window.__detailDelay=false;window.__resolveDetail();await new Promise(r=>setTimeout(r,30));result.stale=!document.querySelector('#detail-content');return result;})()`);
  assert.equal(loading.close,true); assert.equal(loading.focus,'detail-close'); assert.equal(loading.inert,true); assert.equal(loading.lock,'hidden'); assert.equal(loading.restored,true); assert.equal(loading.unlocked,''); assert.equal(loading.stale,true);
  const failureClose=await evaluate(`(async()=>{document.querySelector('.wall-title').focus();window.__detailFail=true;await openDetail({id:1});const r=document.querySelector('#detail-close').getBoundingClientRect();return {x:r.x+r.width/2,y:r.y+r.height/2};})()`);
  await call('Input.dispatchTouchEvent',{type:'touchStart',touchPoints:[failureClose]});
  await call('Input.dispatchTouchEvent',{type:'touchEnd',touchPoints:[]});
  const exited=await evaluate(`({hidden:document.querySelector('#detail-overlay').hidden,focus:document.activeElement.classList.contains('wall-title'),inert:document.querySelector('#app-shell').inert,overflow:document.body.style.overflow})`);
  assert.equal(exited.hidden,true); assert.equal(exited.focus,true); assert.equal(exited.inert,false); assert.equal(exited.overflow,'');
  const failed=await evaluate(`(async()=>{window.__detailFail=true;await openDetail({id:1});const result={retry:!!document.querySelector('#detail-retry'),close:!!document.querySelector('#detail-close'),focus:document.activeElement.id};window.__detailFail=false;document.querySelector('#detail-retry').click();await new Promise(r=>setTimeout(r,40));result.loaded=!!document.querySelector('.detail-plot');const drawer=document.querySelector('#detail-drawer');drawer.scrollTop=drawer.scrollHeight;const rect=document.querySelector('#detail-close').getBoundingClientRect();result.closeVisible=rect.top>=0&&rect.bottom<innerHeight;toast('持久操作错误','error');result.feedback=document.querySelector('#detail-overlay .overlay-feedback').textContent;await new Promise(r=>setTimeout(r,3500));result.persistent=document.querySelector('#detail-overlay .overlay-feedback').textContent;closeDetail();result.inert=document.querySelector('#app-shell').inert;return result;})()`);
  assert.equal(failed.retry,true);assert.equal(failed.close,true);assert.equal(failed.focus,'detail-close');assert.equal(failed.loaded,true);assert.equal(failed.closeVisible,true);assert.equal(failed.feedback,'持久操作错误');assert.equal(failed.persistent,'持久操作错误');assert.equal(failed.inert,false);
  const longFeedback=await evaluate(`(async()=>{await openDetail({id:1});toast('长错误反馈'.repeat(500),'error');const feedback=document.querySelector('#detail-overlay .overlay-feedback');const height=feedback.getBoundingClientRect().height;feedback.scrollTop=feedback.scrollHeight;const close=document.querySelector('#detail-close').getBoundingClientRect();const result={height,limit:innerHeight*.25,readable:feedback.scrollHeight>feedback.clientHeight,close:close.top>=0&&close.bottom<innerHeight};closeDetail();return result;})()`);
  assert.ok(longFeedback.height<=longFeedback.limit+1); assert.equal(longFeedback.readable,true); assert.equal(longFeedback.close,true);
  const contrast=await evaluate(`(()=>{const b=document.createElement('button');b.className='btn btn-accent';document.body.appendChild(b);const style=getComputedStyle(b);const result={fg:style.color,bg:style.backgroundColor};b.remove();return result;})()`);
  const lum=color=>{const rgb=color.match(/\d+/g).slice(0,3).map(n=>{const c=Number(n)/255;return c<=.04045?c/12.92:((c+.055)/1.055)**2.4});return .2126*rgb[0]+.7152*rgb[1]+.0722*rgb[2]};
  assert.ok((Math.max(lum(contrast.fg),lum(contrast.bg))+.05)/(Math.min(lum(contrast.fg),lum(contrast.bg))+.05)>=4.5);
});
