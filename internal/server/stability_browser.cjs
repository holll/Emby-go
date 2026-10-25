'use strict';
// Live ephemeral App supplied by the Go test. No intercepted/stubbed fetch APIs.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawn } = require('node:child_process');

async function main() {
  const base = new URL(process.env.EMBY_STABILITY_URL);
  assert.equal(base.protocol, 'http:');
  assert.ok(['127.0.0.1', '[::1]'].includes(base.hostname), 'only ephemeral loopback service');
  const request = async (route, body, token) => {
    const response = await fetch(new URL(route, base), { method: body === undefined ? 'GET' : 'POST', headers: {'Content-Type':'application/json', ...(token ? {'X-Emby-Token':token} : {})}, ...(body === undefined ? {} : {body:JSON.stringify(body)}), signal:AbortSignal.timeout(10000) });
    assert.ok(response.ok, `${route}: HTTP ${response.status}`);
    return response.status === 204 ? null : response.json();
  };
  await request('/api/auth/initialize', {Username:'mobile-admin', Pw:'mobile-stability-password'});
  const login = await request('/Users/AuthenticateByName', {Username:'mobile-admin', Pw:'mobile-stability-password'});
  assert.equal(typeof login.AccessToken, 'string');
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'emby-live-browser-'));
  const proc = spawn(process.env.CHROME_BIN, ['--headless','--no-sandbox','--disable-gpu','--no-first-run','--remote-debugging-port=0','--user-data-dir='+path.join(dir,'profile'),'about:blank']);
  let ws;
  const interrupted = () => { proc.kill(); if (ws) ws.close(); };
  process.once('SIGINT', interrupted);
  try {
    const url = await new Promise((resolve,reject) => {
      let text=''; const timer=setTimeout(()=>reject(Error('Chromium startup deadline')),15000);
      proc.once('error', e=>{clearTimeout(timer);reject(e)});
      proc.once('exit', code=>{clearTimeout(timer);reject(Error('Chromium exited '+code))});
      proc.stderr.on('data', chunk=>{text+=chunk;const m=text.match(/DevTools listening on (ws:\/\/[^\s]+)/);if(m){clearTimeout(timer);resolve(m[1])}});
    });
    ws = new WebSocket(url);
    await new Promise((resolve,reject)=>{ws.addEventListener('open',resolve,{once:true});ws.addEventListener('error',reject,{once:true})});
    let serial=0; const pending=new Map();
    ws.addEventListener('message', event=>{const m=JSON.parse(event.data),p=pending.get(m.id);if(!p)return;pending.delete(m.id);clearTimeout(p.timer);if(m.error)p.reject(Error(JSON.stringify(m.error)));else p.resolve(m.result)});
    ws.addEventListener('close',()=>{for(const p of pending.values()){clearTimeout(p.timer);p.reject(Error('Chromium session closed'))}pending.clear()});
    const send=(method,params={},sessionId)=>new Promise((resolve,reject)=>{const id=++serial;const timer=setTimeout(()=>{pending.delete(id);reject(Error('CDP deadline: '+method))},15000);pending.set(id,{resolve,reject,timer});ws.send(JSON.stringify({id,method,params,...(sessionId?{sessionId}:{})}))});
    const {targetId}=await send('Target.createTarget',{url:'about:blank'});
    const {sessionId}=await send('Target.attachToTarget',{targetId,flatten:true});
    const call=(method,params)=>send(method,params,sessionId);
    const evaluate=async expression=>{const r=await call('Runtime.evaluate',{expression,awaitPromise:true,returnByValue:true});if(r.exceptionDetails)throw Error(JSON.stringify(r.exceptionDetails));return r.result.value};
    await call('Page.enable');
    await call('Emulation.setDeviceMetricsOverride',{width:390,height:844,deviceScaleFactor:1,mobile:true});
    await call('Emulation.setEmulatedMedia',{features:[{name:'prefers-reduced-motion',value:'reduce'}]});
    // Seed only a real authenticated session. Native fetch, HTML, JS, CSS, and every
    // management response are served by the actual Go App over TCP.
    await call('Page.addScriptToEvaluateOnNewDocument',{source:`if(location.origin===${JSON.stringify(base.origin)}){localStorage.setItem('emby_token',${JSON.stringify(login.AccessToken)});window.__errors=[];addEventListener('error',e=>__errors.push(e.message));}`});
    await call('Page.navigate',{url:new URL('/admin',base).href});
    const wait=expression=>evaluate(`(async()=>{const end=Date.now()+12000;while(Date.now()<end){if(${expression})return true;await new Promise(r=>setTimeout(r,25));}throw Error('UI deadline: '+document.body.innerText.slice(0,800));})()`);
    await wait("document.querySelector('#overview-dashboard')");
    await wait("document.querySelector('[data-dash=\"requests\"]')?.textContent!=='—' && document.querySelector('[data-dash=\"requests\"]')");
    const liveSnapshot=await request('/api/admin/dashboard?window=15m',undefined,login.AccessToken);
    assert.ok(liveSnapshot.requests.total>0,'real login/JSON requests are measured');
    assert.ok(liveSnapshot.requests.p95_ms>0 || liveSnapshot.requests.p95_overflow,'real latency samples, not demo values');
    assert.equal(liveSnapshot.series.length,15);
    for(const width of [1440,768,390,360,320]){
      await call('Emulation.setDeviceMetricsOverride',{width,height:900,deviceScaleFactor:1,mobile:width<860});
      await wait("[...document.querySelectorAll('.dash-axis')].length>0 && [...document.querySelectorAll('.dash-axis')].every(n=>parseFloat(getComputedStyle(n).fontSize)*n.getScreenCTM().a>=11.99)");
      const state=await evaluate("(()=>{const r=document.querySelector('#overview-dashboard');return {root:!!r,overflow:document.documentElement.scrollWidth>innerWidth+1,p95:document.querySelector('[data-dash=\"p95\"]').textContent,charts:r.querySelectorAll('svg[role=\"img\"]').length,axes:[...r.querySelectorAll('.dash-axis')].map(n=>parseFloat(getComputedStyle(n).fontSize)*n.getScreenCTM().a),controls:[...r.querySelectorAll('.dash-toolbar button')].filter(b=>b.getClientRects().length).map(b=>{const q=b.getBoundingClientRect();return {width:q.width,height:q.height}}),errors:__errors.slice()};})()");
      assert.equal(state.root,true);assert.equal(state.overflow,false,'dashboard width '+width);assert.notEqual(state.p95,'—');assert.equal(state.charts,2);assert.ok(state.axes.every(size=>size>=11.99),'axis CSS size '+width);assert.deepEqual(state.errors,[]);
      assert.ok(state.controls.every(b=>b.width>=43.99&&b.height>=43.99),'dashboard touch targets '+width);
    }
    await evaluate("document.querySelector('[data-window=\"5\"]').click()");
    await wait("document.querySelectorAll('[data-dash=\"samples\"] tr').length===5");
    await evaluate("document.querySelector('[data-dash=\"pause\"]').click()");
    assert.equal(await evaluate("document.querySelector('[data-dash=\"pause\"]').getAttribute('aria-pressed')"),'true');
    await evaluate("document.querySelector('[data-dash=\"wide\"]').click()");
    await wait("document.body.classList.contains('dashboard-wide')");
    await evaluate("document.dispatchEvent(new KeyboardEvent('keydown',{key:'Escape',bubbles:true}))");
    assert.equal(await evaluate("document.body.classList.contains('dashboard-wide')"),false);
    await call('Emulation.setDeviceMetricsOverride',{width:390,height:844,deviceScaleFactor:1,mobile:true});
    await evaluate("(async()=>{await page('libraries');document.querySelector('#lib-name').value='Mobile live library';document.querySelector('#lib-path').value="+JSON.stringify(process.env.EMBY_STABILITY_LIBRARY)+";document.querySelector('#library-form').requestSubmit();})()");
    await wait("document.querySelector('[data-lib-scan]')");
    const libraries=await request('/api/admin/libraries',undefined,login.AccessToken);
    assert.equal(libraries.items.length,1,'mobile form committed to real SQLite');
    assert.equal(libraries.items[0].Name,'Mobile live library');
    // A real tap on the rendered scan button starts the same async task as a user.
    const hit=await evaluate("(()=>{const b=document.querySelector('[data-lib-scan]');b.scrollIntoView({block:'center'});const r=b.getBoundingClientRect(),h=document.elementFromPoint(r.x+r.width/2,r.y+r.height/2);return {x:r.x+r.width/2,y:r.y+r.height/2,ok:h===b||b.contains(h)};})()");
    assert.ok(hit.ok,'scan control is hit-testable');
    await call('Input.dispatchMouseEvent',{type:'mousePressed',x:hit.x,y:hit.y,button:'left',clickCount:1});
    await call('Input.dispatchMouseEvent',{type:'mouseReleased',x:hit.x,y:hit.y,button:'left',clickCount:1});
    const end=Date.now()+15000; let items;
    while(Date.now()<end){items=await request('/api/admin/items',undefined,login.AccessToken);if(items.items?.length===1)break;await new Promise(r=>setTimeout(r,40))}
    assert.equal(items.items?.length,1,'mobile scan indexed fixture');
    await evaluate("page('items')");
    await wait("document.querySelector('.wall-card')");
    assert.match(await evaluate("document.querySelector('#content').innerText"),/Mobile stability sample/);
    const menu=await evaluate("(()=>{const b=document.querySelector('#nav-toggle');b.focus();b.click();const opened=b.getAttribute('aria-expanded')==='true';document.dispatchEvent(new KeyboardEvent('keydown',{key:'Escape',bubbles:true}));return {opened,closed:b.getAttribute('aria-expanded')==='false',restored:document.activeElement===b,overflow:document.documentElement.scrollWidth>innerWidth+1,errors:__errors};})()");
    assert.equal(menu.opened,true);assert.equal(menu.closed,true);assert.equal(menu.restored,true);assert.equal(menu.overflow,false);assert.deepEqual(menu.errors,[]);
    await evaluate("page('scheduled')");
    await wait("document.querySelector('#sched-form')");
    await evaluate(`(()=>{document.querySelector('#scheduled-editor').open=true;document.querySelector('#s-name').value='Isolated mobile schedule';document.querySelector('#s-type').value='scan';document.querySelector('#s-type').dispatchEvent(new Event('change'));document.querySelector('#s-cron').value='0 3 * * *';document.querySelector('#s-enabled').checked=false;document.querySelector('#sched-form').requestSubmit();})()`);
    await wait("document.querySelector('[data-role=\"run\"]')");
    await evaluate("document.querySelector('[data-role=\"edit\"]').click()");
    await wait("document.activeElement.id==='s-name'");
    await evaluate("document.querySelector('#s-name').value='Updated isolated schedule';document.querySelector('#sched-form').requestSubmit()");
    await wait("document.querySelector('.scheduled-table').textContent.includes('Updated isolated schedule')");
    assert.equal(await evaluate("document.activeElement.dataset.role"),'edit');
    await evaluate("document.querySelector('[data-role=\"run\"]').click()");
    await wait("document.querySelector('#scheduled-observation').textContent.includes('请求已接受')");
    assert.match(await evaluate("document.querySelector('#scheduled-observation').textContent"),/不等于执行成功/);
    await wait("document.querySelector('#task-log').textContent.includes('成功') || document.querySelector('#task-log').textContent.includes('进行中')");
    await evaluate("page('overview')");
    await wait("document.querySelector('#overview-dashboard')");
    assert.equal(await evaluate("scheduledWatchStop"),null);
    if(process.env.EMBY_DASHBOARD_ARTIFACT_DIR){
      // Optional review artifacts contain only this temporary test service, not demo telemetry or production data.
      const output=path.resolve(process.env.EMBY_DASHBOARD_ARTIFACT_DIR);fs.mkdirSync(output,{recursive:true});
      await new Promise(r=>setTimeout(r,16000));
      await request('/api/admin/dashboard?window=15m',undefined,login.AccessToken);
      await evaluate("page('overview')");
      await wait("document.querySelector('[data-dash=\"requests\"]')?.textContent!=='—' && document.querySelector('[data-dash=\"requests\"]')");
      for(const width of [1440,390]){
        await call('Emulation.setDeviceMetricsOverride',{width,height:1000,deviceScaleFactor:1,mobile:width<860});
        const metrics=await call('Page.getLayoutMetrics');
        const shot=await call('Page.captureScreenshot',{format:'png',captureBeyondViewport:true,clip:{x:0,y:0,width,height:Math.min(Math.ceil(metrics.cssContentSize.height),5000),scale:1}});
        fs.writeFileSync(path.join(output,'dashboard-'+width+'.png'),Buffer.from(shot.data,'base64'));
      }
    }
    console.log('PASS real HTTP dashboard at 1440/768/390/360/320px: real request P95, window switching, pause/fullscreen cleanup; mobile create library → tap scan → indexed media wall');
  } finally {
    process.removeListener('SIGINT', interrupted);
    if(ws)ws.close();
    proc.kill();
    await new Promise(resolve=>{if(proc.exitCode!==null||proc.signalCode)resolve();else proc.once('exit',resolve)});
    assert.equal(path.dirname(dir),os.tmpdir());assert.ok(path.basename(dir).startsWith('emby-live-browser-'));
    fs.rmSync(dir,{recursive:true,force:true});
  }
}
main().catch(error=>{console.error(error.stack);process.exitCode=1});
