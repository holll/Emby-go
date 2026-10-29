'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');
const source = fs.readFileSync(path.join(__dirname, 'web/sprites.js'), 'utf8');
const flush = async () => { for (let i=0;i<40;i++) await Promise.resolve(); };
function fixture(api, options={}) {
  let current=true, next=0, completed=0; const nodes=new Map(), calls=[], timers=new Map();
  function node(key) { if(!nodes.has(key)) nodes.set(key,{value:'',checked:false,hidden:false,disabled:false,textContent:'',events:new Map(),addEventListener(e,fn){this.events.set(e,fn);},removeEventListener(e){this.events.delete(e);}});return nodes.get(key); }
  node('interval').value='10';
  const host={isConnected:true,innerHTML:'',querySelector:s=>node(s.match(/data-sprite="([^"]+)"/)[1])};
  const box=options.box||vm.createContext({AbortController,console,setTimeout(fn,ms){const id=++next;timers.set(id,{fn,ms});return id;},clearTimeout(id){timers.delete(id);}});
  if(!options.box)vm.runInContext(source,box);
  const dispose=box.mountSprites(host,{movie:{id:19},confirm:options.confirm||(()=>true),isCurrent:()=>current,onComplete:()=>completed++,apiRequest:(p,o)=>{calls.push([p,o]);return p==='/sprites/capabilities?movie_id=19'?Promise.resolve({enabled:options.enabled!==false,interval_seconds:options.interval??10}):p==='/sprites/status'?Promise.resolve(options.latest||{task_id:0,instance_id:'instance',running:false,movie_ids:null}):api(p,o);}});
  return {box,host,node,calls,timers,dispose,completed:()=>completed,stale:()=>{current=false;},click:key=>node(key).events.get('click')?.(),async tick(ms){const entry=[...timers].find(([,t])=>t.ms===ms);assert.ok(entry,'timer '+ms);timers.delete(entry[0]);entry[1].fn();await flush();}};
}
const status=extra=>({task_id:8,instance_id:'instance',running:false,movie_ids:[19],done:1,total:1,success:1,...extra});
test('generation defaults protect old previews and no-confirm never posts',async()=>{
  const f=fixture(async p=>p.endsWith('/run')?{task_id:8,instance_id:'instance'}:status());await flush();f.click('generate');await flush();
  assert.deepEqual(JSON.parse(f.calls.find(([p])=>p.endsWith('/run'))[1].body),{movie_ids:[19],overwrite:false});assert.equal(f.completed(),1);f.dispose();
  const g=fixture(async()=>{throw Error('unexpected');},{confirm:()=>false});await flush();g.click('generate');await flush();assert.equal(g.calls.length,2);g.dispose();
});
test('detail shows inherited library density without an editable interval or override',async()=>{
  const f=fixture(async p=>p.endsWith('/run')?{task_id:8,instance_id:'instance'}:status(),{interval:300});await flush();assert.match(f.node('density').textContent,/300 秒/);assert.doesNotMatch(f.host.innerHTML,/data-sprite="interval"/);assert.match(f.host.innerHTML,/href="#libraries"/);f.click('generate');await flush();assert.deepEqual(JSON.parse(f.calls.find(([p])=>p.endsWith('/run'))[1].body),{movie_ids:[19],overwrite:false});f.dispose();
  for(const interval of [0,9,86401,1.5,'<svg onload=x>']){const g=fixture(async()=>{throw Error('unexpected');},{interval});await flush();assert.equal(g.node('generate').disabled,true);assert.equal(g.calls.length,1);assert.doesNotMatch(g.node('error').textContent,/<svg/);g.dispose();}
});
test('running task resumes after close and terminal callback is compound-identity deduplicated',async()=>{
  const latest=status({running:true,success:0});const f=fixture(async()=>status({running:true}),{latest});await flush();assert.equal(f.node('cancel').hidden,false);f.dispose();assert.equal(f.timers.size,0);
  const g=fixture(async()=>status(),{latest,box:f.box});await flush();assert.equal(g.completed(),1);g.dispose();const h=fixture(async()=>status(),{latest,box:f.box});await flush();assert.equal(h.completed(),0);h.dispose();
});
test('ambiguous submit stays disabled across close and only recovers newer matching task',async()=>{
  const f=fixture(async()=>{throw Error('network');});await flush();f.click('generate');await flush();assert.equal(f.node('generate').disabled,true);f.dispose();
  const g=fixture(async()=>status(),{box:f.box});await flush();assert.equal(g.node('generate').disabled,true);assert.equal(g.calls.some(([p])=>p.endsWith('/run')),false);g.dispose();
  const h=fixture(async()=>status(),{box:f.box,latest:status({running:true})});await flush();assert.equal(h.completed(),1);assert.equal(h.node('generate').disabled,false);h.dispose();
});
test('unrelated task cannot be cancelled and does not complete this movie',async()=>{
  const f=fixture(async()=>status({running:true,movie_ids:[20]}),{latest:status({running:true,movie_ids:[20]})});await flush();assert.equal(f.node('cancel').hidden,true);f.click('cancel');await flush();assert.equal(f.calls.some(([p])=>p.endsWith('/cancel')),false);assert.equal(f.completed(),0);f.dispose();
});
test('cancel uses exact instance identity and continues polling until terminal',async()=>{
  let cancelled=false,reads=0;const f=fixture(async(p,o)=>{if(p.endsWith('/cancel')){cancelled=true;assert.deepEqual(JSON.parse(o.body),{task_id:8,instance_id:'instance'});return{task_id:8,instance_id:'instance'};}reads++;return status({running:!cancelled,success:0,cancelled});},{latest:status({running:true,success:0})});await flush();f.click('cancel');await flush();assert.ok(reads>=2);assert.equal(f.node('cancel').hidden,true);assert.equal(f.completed(),0);assert.match(f.node('state').textContent,/已取消/);f.dispose();
});
test('mismatched identity disables cancellation; stale host and close abort reads',async()=>{
  const f=fixture(async()=>status({instance_id:'other'}),{latest:status({running:true})});await flush();assert.equal(f.node('cancel').disabled,true);f.dispose();
  let signal;const g=fixture((p,o)=>{signal=o.signal;return new Promise(()=>{});},{latest:status({running:true})});await flush();assert.ok(signal);g.dispose();assert.equal(signal.aborted,true);assert.equal(g.timers.size,0);
});
test('capability disable and rejected POST never falsely report successful completion',async()=>{
  const f=fixture(async()=>{throw Error('unexpected');},{enabled:false});await flush();assert.equal(f.node('generate').disabled,true);f.dispose();
  const g=fixture(async()=>{throw Object.assign(Error('<img onerror=alert(1)>'),{status:403});});await flush();g.click('generate');await flush();assert.equal(g.completed(),0);assert.doesNotMatch(g.node('error').textContent,/<img/);assert.equal(g.node('generate').disabled,false);g.dispose();
});
function manifest(count=205) {const generation='a'.repeat(32);return{version:1,generation,width:320,height:180,columns:10,rows:10,count,interval_ms:10000,duration_ms:count*10000,sheet_count:Math.ceil(count/100),sheets:Array.from({length:Math.ceil(count/100)},(_,i)=>({url:'/api/items/19/sprites/sheets/'+i+'?generation='+generation,width:3200,height:1800,count:Math.min(100,count-i*100)}))};}
function helpers(){const box=vm.createContext({});vm.runInContext(source,box);return box.SpritePreview;}
test('precise frame lookup crosses rows and sheets without percentage approximation',()=>{
  const p=helpers(),m=p.validateManifest(manifest(),19);
  const frame=(ms)=>JSON.parse(JSON.stringify(p.frameAt(m,ms)));
  assert.deepEqual(frame(0),{index:0,sheet:0,x:0,y:0,start_ms:0,end_ms:10000});
  assert.equal(frame(9999).index,0);assert.equal(frame(10000).index,1);assert.equal(frame(999999).index,99);
  assert.deepEqual(frame(1000000),{index:100,sheet:1,x:0,y:0,start_ms:1000000,end_ms:1010000});
  assert.deepEqual(frame(2050000),{index:204,sheet:2,x:1280,y:0,start_ms:2040000,end_ms:2050000});
  assert.equal(frame(-100).index,0);assert.equal(frame(NaN).index,0);assert.equal(frame(1e20).index,204);
});
test('manifest enforces the backend 600-frame and six-sheet allocation ceiling',()=>{
  const p=helpers();assert.equal(p.validateManifest(manifest(600),19).sheets.length,6);assert.throws(()=>p.validateManifest(manifest(601),19));
});
test('manifest rejects external, traversal, token-bearing and mismatched generation URLs',()=>{
  const p=helpers();for(const url of ['https://evil.test/a','//evil.test/a','/api/items/19/sprites/sheets/../0?generation='+'a'.repeat(32),'/api/items/19/sprites/sheets/0?generation='+'a'.repeat(32)+'&api_key=secret','/api/items/20/sprites/sheets/0?generation='+'a'.repeat(32),'/api/items/19/sprites/sheets/0?generation='+'b'.repeat(32)]){const m=manifest();m.sheets[0].url=url;assert.throws(()=>p.validateManifest(m,19));}
});
test('manifest rejects invalid shape, counts, dimensions, timestamps and generation injection',()=>{
  const p=helpers();for(const change of [{count:206},{sheet_count:2},{width:0},{height:1.5},{columns:0},{rows:1000},{interval_ms:0},{duration_ms:0},{generation:'<svg onload=x>'},{version:2}])assert.throws(()=>p.validateManifest({...manifest(),...change},19));
  for(const change of [{count:99},{width:8192,height:8192},{width:3199},{height:1799}]){const m=manifest();Object.assign(m.sheets[0],change);assert.throws(()=>p.validateManifest(m,19));}assert.throws(()=>p.validateManifest(manifest(),'19/../20'));
});
function previewFixture() {
  const nodes=[], timers=new Map(), requests=[], decoding=[], revoked=[], handlers=new Map();let next=0,urlID=0;
  function element(tag){const e={tag,hidden:false,style:{},children:[],events:new Map(),clientWidth:400,clientHeight:300,append(...v){this.children.push(...v);},appendChild(v){this.children.push(v);},setAttribute(k,v){this[k]=v;},addEventListener(k,fn){this.events.set(k,fn);},removeEventListener(k){this.events.delete(k);},remove(){this.removed=true;},querySelector(s){if(!this.parts)this.parts=new Map();if(!this.parts.has(s))this.parts.set(s,element(s));return this.parts.get(s);}};nodes.push(e);return e;}
  const player=element('player'),stage={after(e){this.rail=e;},parentElement:{classList:{add(){},remove(){}}}};
  class ImageStub {constructor(){this.naturalWidth=3200;this.naturalHeight=1800;decoding.push(this);}set src(v){this.url=v;}}
  const box=vm.createContext({AbortController,console,document:{createElement:element},Image:ImageStub,getComputedStyle:()=>({bottom:'64px'}),URL:{createObjectURL:()=> 'blob:sheet-'+(++urlID),revokeObjectURL:u=>revoked.push(u)},setTimeout(fn,ms){const id=++next;timers.set(id,{fn,ms});return id;},clearTimeout(id){timers.delete(id);}});vm.runInContext(source,box);
  const art={template:{$player:player},on(k,fn){handlers.set(k,fn);},off(k){handlers.delete(k);}};
  const dispose=box.SpritePreview.mount(art,{movieID:19,stage,requestJSON:async()=>manifest(),requestImage:(url,{signal})=>new Promise(resolve=>requests.push({url,signal,resolve})),isCurrent:()=>true});
  return{box,art,player,stage,nodes,timers,requests,decoding,revoked,handlers,dispose,hover:f=>handlers.get('setBar')('hover',f,{type:'mousemove'})};
}
test('racing sheet decodes keep the requested Blob alive, cache bounded and destroy fully cleans up',async()=>{
  const f=previewFixture();await flush();f.hover(0.01);f.hover(0.51);f.hover(0.999);await flush();assert.equal(f.requests.length,3);
  for(const request of f.requests)request.resolve({type:'image/jpeg',size:500});await flush();assert.equal(f.decoding.length,3);
  for(const image of f.decoding)image.onload();await flush();
  const picture=f.nodes.find(n=>n.className==='sprites-player-picture'),bubble=f.nodes.find(n=>n.className==='sprites-player-preview');assert.equal(bubble.hidden,false);assert.match(picture.style.backgroundImage,/blob:sheet-3/);assert.equal(f.revoked.includes('blob:sheet-3'),false);assert.ok(f.decoding.length-f.revoked.length<=2);f.dispose();assert.equal(f.timers.size,0);assert.equal(f.handlers.size,0);assert.equal(new Set(f.revoked).size,3);assert.equal(bubble.removed,true);
});
test('closing preview aborts pending image GET and prevents late decode/render',async()=>{
  const f=previewFixture();await flush();f.hover(0.1);await flush();assert.equal(f.requests.length,1);f.dispose();assert.equal(f.requests[0].signal.aborted,true);f.requests[0].resolve({type:'image/jpeg',size:500});await flush();assert.equal(f.decoding.length,0);assert.equal(f.timers.size,0);
});
test('all cache URLs are Blob-only, authenticated app integration has no URL token and vendor remains unused',()=>{
  const app=fs.readFileSync(path.join(__dirname,'web/app.js'),'utf8');assert.match(app,/SpritePreview\.mount/);assert.match(app,/requestImage: \(path, options\) => request\(path, options, true\)/);assert.match(app,/headers: \{ 'X-Emby-Token': identity \}/);assert.match(source,/global\.URL\.revokeObjectURL/);assert.doesNotMatch(source,/api_key=|thumbnail:\s*\{/);
});
