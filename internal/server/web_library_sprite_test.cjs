'use strict';
const test=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path'),vm=require('node:vm');
const source=fs.readFileSync(path.join(__dirname,'web/sprites.js'),'utf8');
const flush=async()=>{for(let i=0;i<35;i++)await Promise.resolve();};
function fixture(api=async(p,o)=>JSON.parse(o.body),options={}){
  const nodes=new Map(),calls=[],timers=new Map();let current=true,next=0,saved=[];
  function node(key){if(!nodes.has(key))nodes.set(key,{value:'',hidden:false,disabled:false,textContent:'',events:new Map(),attrs:new Map(),focus(){this.focused=true;},setAttribute(k,v){this.attrs.set(k,v);},removeAttribute(k){this.attrs.delete(k);},addEventListener(k,v){this.events.set(k,v);},removeEventListener(k){this.events.delete(k);}});return nodes.get(key);}
  const host={isConnected:true,innerHTML:'',querySelector:s=>node(s==='form'?'form':s.match(/data-library-sprite="([^"]+)"/)[1])};
  const box=vm.createContext({AbortController,setTimeout(fn,ms){const id=++next;timers.set(id,{fn,ms});return id;},clearTimeout(id){timers.delete(id);}});vm.runInContext(source,box);
  const dispose=box.mountLibrarySprites(host,{library:{Id:7,Name:options.name||'电影库',sprite_interval_seconds:options.interval??10},apiRequest:(p,o)=>{calls.push([p,o]);return api(p,o);},isCurrent:()=>current,onSaved:v=>saved.push(v)});
  return{host,node,calls,timers,dispose,saved,stale:()=>{current=false;},preset(value){node('preset').value=value;node('preset').events.get('change')();},submit:()=>node('form').events.get('submit')({preventDefault(){}}),async tick(){const [id,t]=[...timers][0];timers.delete(id);t.fn();await flush();}};
}
test('library density presets initialize default, known and custom saved values without mutations',()=>{
  for(const [interval,preset]of[[10,'10'],[60,'60'],[300,'300'],[45,'custom']]){const f=fixture(undefined,{interval});assert.equal(f.node('preset').value,preset);assert.equal(f.node('custom-field').hidden,preset!=='custom');assert.match(f.node('state').textContent,new RegExp(interval+' 秒'));assert.equal(f.calls.length,0);f.dispose();}
});
test('presets and custom boundary values save strict library-only PUT bodies',async()=>{
  for(const value of [300,60,10,45,86400]){const f=fixture();f.preset([300,60,10].includes(value)?String(value):'custom');f.node('interval').value=String(value);await f.submit();assert.equal(f.calls[0][0],'/libraries/7/sprites');assert.equal(f.calls[0][1].method,'PUT');assert.deepEqual(JSON.parse(f.calls[0][1].body),{interval_seconds:value});assert.deepEqual(f.saved,[value]);assert.match(f.node('state').textContent,/现有预览保持不变/);assert.equal(f.calls.length,1);f.dispose();}
});
test('invalid custom values keep drafts and focus without any network request',async()=>{
  for(const value of ['', '9','0','86401','10.5','NaN','Infinity']){const f=fixture();f.preset('custom');f.node('interval').value=value;await f.submit();assert.equal(f.node('interval').value,value);assert.equal(f.node('interval').focused,true);assert.equal(f.node('interval').attrs.get('aria-invalid'),'true');assert.equal(f.node('error').hidden,false);assert.equal(f.calls.length,0);f.dispose();}
});
test('pending save disables controls and rejects duplicate submissions',async()=>{
  let resolve;const f=fixture(()=>new Promise(r=>resolve=r));f.preset('60');f.submit();f.submit();await flush();assert.equal(f.calls.length,1);for(const k of ['preset','interval','save'])assert.equal(f.node(k).disabled,true);resolve({interval_seconds:60});await flush();assert.deepEqual(f.saved,[60]);assert.equal(f.node('save').disabled,false);assert.equal(f.timers.size,0);f.dispose();
});
test('save errors preserve custom draft and never leak backend markup or secrets',async()=>{
  const f=fixture(async()=>{throw Error('<svg onload=alert(1)> token=secret');});f.preset('custom');f.node('interval').value='37';await f.submit();assert.equal(f.node('interval').value,'37');assert.equal(f.node('preset').value,'custom');assert.equal(f.node('save').disabled,false);assert.match(f.node('error').textContent,/草稿已保留/);assert.doesNotMatch(f.node('error').textContent,/<svg|secret/);assert.deepEqual(f.saved,[]);assert.equal(f.calls.length,1);f.dispose();
});
test('manual retry after failed save succeeds without rerendering the draft',async()=>{
  let attempt=0;const f=fixture(async()=>{if(++attempt===1)throw Error('network');return{interval_seconds:47};});f.preset('custom');f.node('interval').value='47';await f.submit();assert.equal(f.calls.length,1);await f.submit();assert.deepEqual(f.saved,[47]);assert.equal(f.node('error').hidden,true);assert.equal(f.node('interval').value,'47');f.dispose();
});
test('timeout aborts the request, retains draft and does not replay automatically',async()=>{
  const f=fixture(()=>new Promise(()=>{}));f.preset('custom');f.node('interval').value='27';f.submit();await f.tick();assert.equal(f.calls[0][1].signal.aborted,true);assert.equal(f.calls.length,1);assert.equal(f.node('interval').value,'27');assert.equal(f.node('save').disabled,false);assert.equal(f.node('error').hidden,false);f.dispose();
});
test('disposed or identity-stale saves never update the new context',async()=>{
  for(const dispose of [false,true]){let resolve;const f=fixture(()=>new Promise(r=>resolve=r));f.preset('300');f.submit();const before=f.node('state').textContent;if(dispose)f.dispose();else f.stale();resolve({interval_seconds:300});await flush();assert.deepEqual(f.saved,[]);assert.equal(f.node('state').textContent,before);if(dispose){assert.equal(f.calls[0][1].signal.aborted,true);assert.equal(f.node('form').events.size,0);}f.dispose();assert.equal(f.timers.size,0);}
});
test('malformed or mismatched save response does not claim settings were persisted',async()=>{
  for(const value of [undefined,9,86401,'60',300]){const f=fixture(async()=>({interval_seconds:value}));f.preset('60');await f.submit();assert.deepEqual(f.saved,[]);assert.equal(f.node('error').hidden,false);assert.match(f.node('state').textContent,/10 秒/);f.dispose();}
});
test('custom draft survives preset toggles and library names are text-only',()=>{
  const f=fixture(undefined,{name:'<img src=x onerror=alert(1)>'});f.preset('custom');f.node('interval').value='71';f.preset('300');f.preset('custom');assert.equal(f.node('interval').value,'71');assert.doesNotMatch(f.host.innerHTML,/<img/);assert.match(f.node('title').textContent,/<img/);f.dispose();
});
test('library app integration checks identity and disposes forms on navigation and session clearing',()=>{
  const app=fs.readFileSync(path.join(__dirname,'web/app.js'),'utf8'),css=fs.readFileSync(path.join(__dirname,'web/sprites.css'),'utf8');assert.match(app,/requestGen === libraryPageGen && token === identity && localStorage\.getItem\('emby_token'\) === identity/);assert.match(app,/mountLibrarySprites\(host/);assert.ok((app.match(/stopLibrarySprites\(\);/g)||[]).length>=4);assert.match(css,/\.library-sprites input, \.library-sprites select[^}]*min-height: 44px/);assert.match(source,/最低 10 秒/);assert.match(source,/不会自动生成或覆盖已有预览/);
});
