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
    await wait("document.querySelector('#reindex')");
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
    console.log('PASS real HTTP mobile 390px: create library → tap scan → indexed media wall; native assets, focus and no overflow');
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
