'use strict';
// Real Chromium + the complete frontend, with local fixture APIs only. No business server.
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { pathToFileURL } = require('node:url');
const { available, browser: launchBrowser } = require('./web_browser_helper.cjs');
const web = path.join(__dirname, 'web');

async function browser(t) {
  const { call, evaluate, navigate, dir } = await launchBrowser(t);
  const fixture = `
    window.__calls = []; window.__errors = []; window.__securityRules=[]; window.__logID=0;
    window.__webhooks={multi_target:true,config_etag:'fixture',targets:[{id:'one',name:'通知目标',enabled:true,url:'https://example.invalid/hook?token=fixture',url_configured:true,events:['item.updated'],format:'json',timeout_seconds:5,allow_private:false}]};
    addEventListener('error', e => __errors.push(e.message));
    localStorage.setItem('emby_token','layout-fixture');
    window.confirm=()=>true;
    window.fetch=async (input, options={})=>{
      const url=String(input), p=url.split('?')[0]; __calls.push({url,method:options.method||'GET',body:options.body});
      let data={};
      if(p==='/Users/Me')data={Name:'owner',Policy:{IsAdministrator:true}};
      else if(p.endsWith('/libraries')){const items=window.__libraryItems||[{Id:1,Name:'测试媒体库',Path:'/media/'+ 'long-path/'.repeat(12),count:1}];data={total:items.length,items};}
      else if(p.endsWith('/items'))data={total:2,items:[{id:1,status:'success',Title:'示例影片：移动端资料管理',Number:'TEST-001',source_path:'/media/测试/TEST-001.strm'},{id:2,status:'pending',Title:'等待处理的影片',source_path:'/media/待处理/TEST-002.strm'}]};
      else if(p.endsWith('/detail')){
        if(window.__detailDelay)await new Promise(r=>window.__resolveDetail=r);
        if(window.__detailFail)return {ok:false,status:503,json:async()=>({error:'详情暂不可用，请重试'})};
        data={movie:{id:1,Title:'长详情验证',Status:'success',Plot:'长简介与文件路径 '.repeat(800)},metatube_id:{saved:'',canonical:'',valid:false},files:[]};
      }
      else if(p.endsWith('/scan/progress'))data=window.__scanProgress || {running:true,library_name:'测试媒体库',done:42,total:120};
      else if(p.endsWith('/diagnostics'))data={generated_at:'2026-11-01T08:00:00Z',app:{version:'overview-fixture',commit:'a'.repeat(40)},store:{path:'/data/'+ 'long-path/'.repeat(16),schema_version:22}};
      else if(p.endsWith('/scrape/settings'))data={metatube_url:'https://metatube.invalid',timeout_seconds:30,concurrency:2,translate:{title:false,summary:false,target_lang:'ZH',api_url:'https://translate.invalid'}};
      else if(p.endsWith('/scrape/candidates'))data={total:12};
      else if(p.endsWith('/scrape/progress'))data={running:false,kind:'scrape',done:0,total:0,failed:0,failures:[]};
      else if(p.endsWith('/scrape/avatars'))data={total:2};
      else if(p.endsWith('/translate/run')){window.__translationTask=7;data={task_id:7,total:1};}
      else if(p.endsWith('/translate/status'))data={running:false,task_id:window.__translationTask||0,done:0,total:0,success:0,failed:0,skipped:0,failures:[]};
      else if(p.endsWith('/settings'))data={listen:':18080',cache:'memory',db_path:'/data/emby.db',session_ttl_days:7};
      else if(p.endsWith('/users'))data={users:[{id:1,name:'owner',is_admin:true,library_ids:[1]}]};
      else if(p.endsWith('/apikeys'))data={total:1,items:[{id:'stable',name:'本地脚本',key_prefix:'prefix',version:1}]};
      else if(p.endsWith('/recovery'))data={items:[]};
      else if(p.endsWith('/webhook/test')){
        window.__lastTestSignal=options.signal;
        if(window.__testDelay)await new Promise(r=>window.__resolveTest=r);
        if(window.__testThrow)throw Error('fixture network failure');
        data=window.__testResult||{status:'sent',duration_ms:37,error_code:'',message:'测试通知已发送'};
      }
      else if(p.endsWith('/webhook/deliveries')){
        data=options.method==='DELETE'?{cleared:0}:{items:[{id:1,event:'item.updated',item_id:1,target_id:'one',status:'sent',error_code:'',created_at:'2026-10-29T12:00:00Z'}],retention:{max_rows:1000,days:7}};
      }
      else if(p.endsWith('/webhook/targets')){
        if(options.method==='PUT'){
          const draft=JSON.parse(options.body),previous=__webhooks.targets;
          __webhooks.targets=draft.targets.map((target,i)=>{
            const old=previous.find(t=>t.id===target.id),address=target.clear_url?'':(target.url||old?.url||'');
            return {...target,id:target.id||'new_'+i,url:address,url_configured:!!address};
          });
          __webhooks.config_etag+='-saved';
        }
        data=structuredClone(__webhooks);
      }
      else if(p.includes('/scheduled/')&&p.endsWith('/toggle')){
        if(window.__toggleDelay)await new Promise(r=>window.__resolveToggle=r);
        if(window.__toggleFail)return {ok:false,status:503,json:async()=>({error:'fixture toggle failure'})};
        window.__scheduledEnabled=!(window.__scheduledEnabled??true);
      }
      else if(p.endsWith('/scheduled'))data={items:[{id:1,name:'夜间扫描',type:'scan',cron:'0 3 * * *',params:'{}',enabled:window.__scheduledEnabled??true,config_etag:'fixture'}],types:{scan:'扫描媒体库',reindex:'重建索引',probe:'媒体信息探测',scrape:'内置刮削',scrape_avatars:'演员头像补全',translate:'独立翻译',sprites:'雪碧图生成'}};
      else if(p.endsWith('/dashboard')){
        // Deliberately synthetic layout fixture; real HTTP telemetry is verified by Go/CDP integration.
        const minutes=Number(new URL(url,'http://fixture').searchParams.get('window')?.replace('m','')||15), now=Date.now();
        data={instance_id:'layout-fixture',started_at:new Date(now-120000).toISOString(),generated_at:new Date(now).toISOString(),retention_minutes:60,window_minutes:minutes,window_start:new Date(now-minutes*60000).toISOString(),window_end:new Date(now).toISOString(),requests:{total:60,errors_4xx:2,errors_5xx:1,rate_per_second:.5,p50_ms:2,p95_ms:20,p50_overflow:false,p95_overflow:false,sampled_seconds:120},latency:{unit:'ms',method:'histogram_upper_bound',max_finite_ms:60000,scope:'json_control_plane'},runtime:{uptime_seconds:120,goroutines:24,heap_alloc_bytes:8388608,heap_inuse_bytes:10485760,go_sys_bytes:16777216,num_gc:3},media:{available:true,stale:false,success:36,manual:4,pending:2,total:42,libraries:window.__libraryItems?.length||1,updated_at:new Date(now).toISOString()},tasks:{running:0,recent:[{id:1,kind:'scan',status:'success',started_at:new Date(now-10000).toISOString(),ended_at:new Date(now-5000).toISOString()}]},series:Array.from({length:minutes},(_,i)=>({start:new Date(now-(minutes-i-1)*60000).toISOString(),observed:i>=minutes-2,requests:i>=minutes-2?30:null,errors_4xx:i>=minutes-2?1:null,errors_5xx:i>=minutes-2?0:null,p50_ms:i>=minutes-2?2:null,p95_ms:i>=minutes-2?20:null,p50_overflow:false,p95_overflow:false}))};
      }
      else if(p.endsWith('/security/ip-blocklist')){
        if(options.method==='POST'){const body=JSON.parse(options.body);if(window.__securityFail)return {ok:false,status:409,json:async()=>({error:'<img src=x onerror=alert(1)>'})};__securityRules.push({...body,created_at:'2026-10-29T12:00:00Z',blocked:3});}
        if(options.method==='DELETE'){const body=JSON.parse(options.body);__securityRules=__securityRules.filter(rule=>rule.network!==body.network);}
        data={items:__securityRules,max_rules:256,total_blocked:3,client_ip:'192.0.2.10',stats_since:'2026-10-29T12:00:00Z'};
      }
      else if(p.endsWith('/login-events'))data={total:1,events:[{name:'<img src=x onerror=alert(1)>',ip:'203.0.113.5',success:false,created_at:'2026-10-29'}]};
      else if(p.endsWith('/playback-events'))data={total:0,items:[]};
      else if(p.endsWith('/probe'))data={items:[{method:'GET',path:'/Items/123/[redacted:123456789abc]',created_at:'2026-10-29'}]};
      else if(p.endsWith('/logs/disk')){const id=++__logID;data={items:[{id:String(id),ts:'2026-10-29T12:00:00Z',level:'info',msg:'auto-log-'+id}],next_after:String(id),next_before:String(id),has_more_before:false};}
      else if(p.endsWith('/tasks'))data={items:window.__taskItems||[]};
      else if(p.endsWith('/logs'))data={items:[]};
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
  await navigate(pathToFileURL(file).href);
  await evaluate(`(async()=>{for(let i=0;i<100;i++){if(document.querySelector('#overview-dashboard'))return;await new Promise(r=>setTimeout(r,20));}throw Error('boot did not render: '+document.body.innerText);})()`);
  return { call, evaluate, dir };
}

test('通知设置：删除风险确认是可点击和键盘切换的复选框，保存受确认保护', { timeout: 60000 }, async t => {
  if (!available(t)) return;
  const { call, evaluate } = await browser(t);
  await evaluate(`(async()=>{await page('settings');document.querySelector('[data-settings-category="notifications"]').click();})()`);
  assert.equal(await evaluate(`document.querySelector('#wh-confirm-delete').type`), 'checkbox');
  await evaluate(`(()=>{document.querySelector('[data-target-index]').open=true;document.querySelector('[data-event="item.deleted"]').click();document.querySelector('#wh-save').click();})()`);
  assert.equal(await evaluate(`__calls.filter(c=>c.method==='PUT').length`), 0);
  assert.match(await evaluate(`document.querySelector('#wh-status').textContent`), /请勾选删除风险确认/);
  await evaluate(`document.querySelector('#wh-confirm-delete').closest('label').click()`);
  assert.equal(await evaluate(`document.querySelector('#wh-confirm-delete').checked`), true);
  await evaluate(`document.querySelector('#wh-confirm-delete').focus()`);
  await call('Input.dispatchKeyEvent', { type:'keyDown', key:' ', code:'Space', windowsVirtualKeyCode:32 });
  await call('Input.dispatchKeyEvent', { type:'keyUp', key:' ', code:'Space', windowsVirtualKeyCode:32 });
  assert.equal(await evaluate(`document.querySelector('#wh-confirm-delete').checked`), false);
  const saved = await evaluate(`(async()=>{document.querySelector('#wh-confirm-delete').click();document.querySelector('#wh-save').click();for(let i=0;i<100&&!document.querySelector('#wh-status').textContent.includes('已保存');i++)await new Promise(r=>setTimeout(r,10));return {calls:__calls.filter(c=>c.method==='PUT'),checked:document.querySelector('#wh-confirm-delete').checked};})()`);
  assert.equal(saved.calls.length, 1);
  const draft = JSON.parse(saved.calls[0].body);
  assert.equal(draft.confirm_delete, true);
  assert.ok(draft.targets[0].events.includes('item.deleted'));
  assert.equal(saved.checked, false, '保存后复位确认');
});

test('通知设置：hidden确认行不占空间，删除与清空后才显示', { timeout: 60000 }, async t => {
  if (!available(t)) return;
  const { call, evaluate } = await browser(t);
  for (const width of [1440, 390]) {
    await call('Emulation.setDeviceMetricsOverride', { width, height:844, deviceScaleFactor:1, mobile:width<860 });
    await evaluate(`(async()=>{await page('settings');document.querySelector('[data-settings-category="notifications"]').click();})()`);
    const initial = await evaluate(`['wh-confirm-remove-row','wh-confirm-empty-row'].map(id=>{const e=document.getElementById(id);return {hidden:e.hidden,display:getComputedStyle(e).display,rects:e.getClientRects().length};})`);
    for (const row of initial) assert.deepEqual(row, { hidden:true, display:'none', rects:0 }, '初始确认行隐藏 '+width);
    const removed = await evaluate(`(()=>{document.querySelector('[data-target-index]').open=true;document.querySelector('[data-remove]').click();return ['wh-confirm-remove-row','wh-confirm-empty-row'].map(id=>{const e=document.getElementById(id);return {hidden:e.hidden,display:getComputedStyle(e).display,rects:e.getClientRects().length};});})()`);
    for (const row of removed) { assert.equal(row.hidden,false); assert.equal(row.display,'flex'); assert.ok(row.rects>0); }
    const opened = await evaluate(`(()=>{document.querySelector('#wh-add').click();return {open:document.querySelector('#wh-dialog').open,emptyHidden:document.querySelector('#wh-confirm-empty-row').hidden,cards:document.querySelectorAll('#wh-targets [data-target-index]').length};})()`);
    assert.deepEqual(opened, {open:true,emptyHidden:false,cards:0}, '打开弹窗不添加空卡片');
    const added = await evaluate(`(()=>{document.querySelector('#wh-draft-name').value='草稿';document.querySelector('#wh-draft-url').value='https://example.invalid/draft';document.querySelector('#wh-dialog [data-event="item.updated"]').checked=true;document.querySelector('#wh-dialog-form').requestSubmit();const e=document.querySelector('#wh-confirm-empty-row');return {hidden:e.hidden,display:getComputedStyle(e).display,rects:e.getClientRects().length};})()`);
    assert.deepEqual(added, { hidden:true, display:'none', rects:0 }, '添加后清空确认重新隐藏');
    assert.equal(await evaluate(`__calls.filter(c=>c.method!=='GET').length`), 0, '仅修改草稿');
  }
});

test('通知设置：动态编码和超时标签关联唯一控件，增删重绘后仍正确', { timeout: 60000 }, async t => {
  if (!available(t)) return;
  const { evaluate } = await browser(t);
  await evaluate(`(async()=>{await page('settings');document.querySelector('[data-settings-category="notifications"]').click();for(let i=0;i<100&&document.querySelector('#wh-save').disabled;i++)await new Promise(r=>setTimeout(r,10));document.querySelector('#wh-add').click();})()`);
  async function checkLabels(expected) {
    const result = await evaluate(`(()=>{const controls=[...document.querySelectorAll('#wh-targets [data-field="format"],#wh-targets [data-field="timeout"]')];return controls.map(e=>({id:e.id,unique:!!e.id&&document.querySelectorAll('[id="'+e.id+'"]').length===1,label:!!e.labels?.length&&[...e.labels].some(l=>l.htmlFor===e.id&&l.control===e)}));})()`);
    assert.equal(result.length, expected);
    assert.ok(result.every(e=>e.id&&e.unique&&e.label), JSON.stringify(result));
  }
  async function addDraft() {
    const modalLabels = await evaluate(`(()=>{const controls=[...document.querySelectorAll('#wh-dialog input,#wh-dialog select')];return controls.every(e=>e.id&&document.querySelectorAll('[id="'+e.id+'"]').length===1&&e.labels.length);})()`);
    assert.equal(modalLabels,true,'弹窗内标签与主页面不冲突');
    await evaluate(`(()=>{document.querySelector('#wh-draft-name').value='草稿';document.querySelector('#wh-draft-url').value='https://example.invalid/draft';document.querySelector('#wh-dialog [data-event="item.updated"]').checked=true;document.querySelector('#wh-dialog-form').requestSubmit();})()`);
    await evaluate(`new Promise(r=>requestAnimationFrame(()=>requestAnimationFrame(r)))`);
  }
  await checkLabels(2);
  await addDraft();
  await checkLabels(4);
  await evaluate(`(()=>{document.querySelector('[data-target-index="0"]').open=true;document.querySelector('[data-target-index="0"] [data-remove]').click();document.querySelector('#wh-add').click();})()`);
  await addDraft();
  await checkLabels(4);
});

test('通知设置：弹窗隔离草稿、取消与Esc归还焦点、小屏布局及明文地址保存重载', {timeout:90000}, async t => {
  if (!available(t)) return;
  const {call,evaluate}=await browser(t);
  const focusFailures=[];
  const key=async (key,code,windowsVirtualKeyCode,modifiers=0)=>{
    await call('Input.dispatchKeyEvent',{type:'keyDown',key,code,windowsVirtualKeyCode,modifiers});
    await call('Input.dispatchKeyEvent',{type:'keyUp',key,code,windowsVirtualKeyCode,modifiers});
  };
  for(const [width,height] of [[1440,1000],[390,844],[320,568],[844,390],[667,280]]){
    await call('Emulation.setDeviceMetricsOverride',{width,height,deviceScaleFactor:1,mobile:width<860});
    await evaluate(`(async()=>{await page('settings');document.querySelector('[data-settings-category="notifications"]').click();})()`);
    for(const cancel of ['wh-dialog-cancel','wh-dialog-close','Escape']){
      const opened=await evaluate(`(()=>{document.querySelector('#wh-add').focus();document.querySelector('#wh-add').click();document.querySelector('#wh-draft-name').value='丢弃草稿';const d=document.querySelector('#wh-dialog'),r=d.getBoundingClientRect();return {open:d.open,focus:document.activeElement.id,cards:document.querySelectorAll('#wh-targets [data-target-index]').length,x:r.x,y:r.y,right:r.right,bottom:r.bottom,overflow:d.scrollWidth>d.clientWidth,scroll:document.documentElement.scrollWidth};})()`);
      assert.equal(opened.open,true);assert.equal(opened.focus,'wh-draft-name');assert.equal(opened.cards,1);
      assert.ok(opened.x>=0&&opened.y>=0&&opened.right<=width+1&&opened.bottom<=height+1,JSON.stringify({width,height,...opened}));
      assert.equal(opened.overflow,false);assert.ok(opened.scroll<=width+1);
      await evaluate(`document.querySelector('#wh-dialog-close').focus()`);
      await key('Tab','Tab',9,8);
      if(!await evaluate(`document.activeElement.matches('#wh-dialog button[type="submit"]')`))focusFailures.push({width,height,direction:'reverse',focus:await evaluate('document.activeElement.outerHTML.slice(0,200)')});
      await evaluate(`document.querySelector('#wh-dialog button[type="submit"]').focus()`);
      await key('Tab','Tab',9);
      if(await evaluate(`document.activeElement.id`)!=='wh-dialog-close')focusFailures.push({width,height,direction:'forward',focus:await evaluate('document.activeElement.outerHTML.slice(0,200)')});
      if(process.env.EMBY_LAYOUT_ARTIFACT_DIR&&cancel==='Escape'){
        const output=path.resolve(process.env.EMBY_LAYOUT_ARTIFACT_DIR);fs.mkdirSync(output,{recursive:true});
        const shot=await call('Page.captureScreenshot',{format:'png'});
        fs.writeFileSync(path.join(output,`webhook-modal-${width}-${height}.png`),Buffer.from(shot.data,'base64'));
      }
      if(cancel==='Escape')await key('Escape','Escape',27);
      else await evaluate(`document.getElementById('${cancel}').click()`);
      await evaluate(`new Promise(r=>requestAnimationFrame(()=>requestAnimationFrame(r)))`);
      assert.deepEqual(await evaluate(`({open:document.querySelector('#wh-dialog').open,body:document.querySelector('#wh-dialog-body').children.length,focus:document.activeElement.id,cards:document.querySelectorAll('#wh-targets [data-target-index]').length})`),{open:false,body:0,focus:'wh-add',cards:1});
    }
  }
  assert.equal(await evaluate(`__calls.filter(c=>c.method!=='GET').length`),0);
  const invalid=await evaluate(`(()=>{document.querySelector('#wh-add').click();const f=document.querySelector('#wh-dialog-form'),q=k=>f.querySelector('[data-field="'+k+'"]');const errors=[];f.requestSubmit();errors.push(document.querySelector('#wh-dialog-status').textContent);q('name').value='新通知';f.requestSubmit();errors.push(document.querySelector('#wh-dialog-status').textContent);f.querySelector('[data-event="item.rate"]').checked=true;for(const url of ['', 'not a url','ftp://example.invalid/hook','javascript:alert(1)']){q('url').value=url;f.requestSubmit();errors.push(document.querySelector('#wh-dialog-status').textContent);}return {errors,open:document.querySelector('#wh-dialog').open,cards:document.querySelectorAll('#wh-targets [data-target-index]').length,calls:__calls.filter(c=>c.method!=='GET').length};})()`);
  assert.ok(invalid.errors.every(Boolean));assert.equal(invalid.open,true);assert.equal(invalid.cards,1);assert.equal(invalid.calls,0);
  await evaluate(`(()=>{document.querySelector('#wh-draft-url').value='https://example.invalid/new?token=plain-fixture';document.querySelector('#wh-draft-format').value='form';document.querySelector('#wh-draft-timeout').value='17';document.querySelector('#wh-draft-allow-private').checked=true;document.querySelector('#wh-dialog-form').requestSubmit();})()`);
  assert.equal(await evaluate(`document.querySelectorAll('#wh-targets [data-target-index]').length`),2);
  assert.equal(await evaluate(`__calls.filter(c=>c.method!=='GET').length`),0,'添加到草稿不保存');
  const saved=await evaluate(`(async()=>{document.querySelector('#wh-save').click();for(let i=0;i<100&&document.querySelector('#wh-save').disabled;i++)await new Promise(r=>setTimeout(r,10));return __calls.filter(c=>c.method==='PUT');})()`);
  assert.equal(saved.length,1);const payload=JSON.parse(saved[0].body);
  assert.equal(payload.targets[1].url,'https://example.invalid/new?token=plain-fixture');assert.equal(payload.targets[1].format,'form');assert.equal(payload.targets[1].timeout_seconds,17);assert.equal(payload.targets[1].allow_private,true);
  await evaluate(`(async()=>{document.querySelector('#wh-reload').click();for(let i=0;i<100&&document.querySelector('#wh-save').disabled;i++)await new Promise(r=>setTimeout(r,10));document.querySelectorAll('#wh-targets details').forEach(d=>d.open=true);})()`);
  const addresses=await evaluate(`[...document.querySelectorAll('#wh-targets [data-field="url"]')].map(e=>({value:e.value,type:e.type,visible:!!e.getClientRects().length}))`);
  assert.deepEqual(addresses,[{value:'https://example.invalid/hook?token=fixture',type:'text',visible:true},{value:'https://example.invalid/new?token=plain-fixture',type:'text',visible:true}]);
  assert.deepEqual(await evaluate('__errors'),[]);
  assert.deepEqual(focusFailures,[],'Tab/Shift+Tab必须始终在弹窗内循环');
});

test('通知设置：显式测试使用当前草稿、旧地址回退、验证和发送结果，不保存配置', {timeout:60000}, async t => {
  if (!available(t)) return;
  const {call,evaluate}=await browser(t);
  await evaluate(`(async()=>{await page('settings');document.querySelector('[data-settings-category="notifications"]').click();for(let i=0;i<100&&!document.querySelector('[data-target-index]');i++)await new Promise(r=>setTimeout(r,10));if(!document.querySelector('[data-target-index]'))throw Error('Webhook fixture did not load: '+document.querySelector('#wh-status')?.textContent+'; '+__errors.join(';'));document.querySelector('[data-target-index]').open=true;})()`);
  assert.equal(await evaluate(`__calls.filter(c=>c.url.split('?')[0].endsWith('/webhook/test')).length`),0,'页面加载不自动发送');
  const before=await evaluate(`JSON.stringify(__webhooks)`);
  for(const [format,url] of [['json','https://example.invalid/unsaved'],['form','']]){
    const result=await evaluate(`(async()=>{const c=document.querySelector('[data-target-index]'),q=k=>c.querySelector('[data-field="'+k+'"]');q('name').value='';q('enabled').checked=false;c.querySelectorAll('[data-event]').forEach(e=>e.checked=false);q('url').value=${JSON.stringify(url)};q('format').value='${format}';q('timeout').value='19';q('allow-private').checked=true;const b=c.querySelector('[data-test]');b.click();for(let i=0;i<100&&b.disabled;i++)await new Promise(r=>setTimeout(r,10));const s=c.querySelector('[data-test-status]');return {type:b.type,text:s.textContent,role:s.getAttribute('role'),calls:__calls.filter(c=>c.url.split('?')[0].endsWith('/webhook/test'))};})()`);
    assert.equal(result.type,'button');assert.ok(['status','alert'].includes(result.role));assert.match(result.text,/37/);assert.match(result.text,/成功|已发送|已获接收端确认/);
    const request=result.calls.at(-1);assert.equal(request.method,'POST');
    assert.deepEqual(JSON.parse(request.body),{url:url||'https://example.invalid/hook?token=fixture',format,timeout_seconds:19,allow_private:true});
  }
  for(const change of [`q('clear-url').checked=true`, `q('clear-url').checked=false;q('url').value='ftp://example.invalid'`, `q('url').value='https://example.invalid';q('timeout').value='0'`]){
    const validation=await evaluate(`(()=>{const c=document.querySelector('[data-target-index]'),q=k=>c.querySelector('[data-field="'+k+'"]');${change};const n=__calls.length;c.querySelector('[data-test]').click();return {count:__calls.length-n,text:c.querySelector('[data-test-status]').textContent};})()`);
    assert.equal(validation.count,0);assert.ok(validation.text);
  }
  const failed=await evaluate(`(async()=>{const c=document.querySelector('[data-target-index]');c.querySelector('[data-field="timeout"]').value='5';__testResult={status:'failed',duration_ms:81,error_code:'delivery_failed',message:'接收端返回失败'};const b=c.querySelector('[data-test]');b.click();for(let i=0;i<100&&b.disabled;i++)await new Promise(r=>setTimeout(r,10));return c.querySelector('[data-test-status]').textContent;})()`);
  assert.match(failed,/81/);assert.match(failed,/失败|未通过/);
  const networkFailure=await evaluate(`(async()=>{__testThrow=true;const c=document.querySelector('[data-target-index]'),b=c.querySelector('[data-test]'),n=__calls.length;b.click();for(let i=0;i<100&&b.disabled;i++)await new Promise(r=>setTimeout(r,10));await new Promise(r=>setTimeout(r,30));__testThrow=false;return {calls:__calls.length-n,text:c.querySelector('[data-test-status]').textContent,role:c.querySelector('[data-test-status]').getAttribute('role')};})()`);
  assert.equal(networkFailure.calls,1,'连接失败也不自动重试');assert.equal(networkFailure.role,'alert');assert.ok(networkFailure.text);assert.doesNotMatch(networkFailure.text,/fixture network failure/);
  assert.equal(await evaluate(`JSON.stringify(__webhooks)`),before);
  assert.equal(await evaluate(`__calls.filter(c=>c.method==='PUT').length`),0);
  for(const width of [390,320]){
    await call('Emulation.setDeviceMetricsOverride',{width,height:568,deviceScaleFactor:1,mobile:true});
    const layout=await evaluate(`(()=>{document.querySelector('#wh-add').click();const c=document.querySelector('#wh-dialog');return {button:c.querySelector('[data-test]').type,role:c.querySelector('[data-test-status]').getAttribute('role'),overflow:c.scrollWidth>c.clientWidth,scroll:document.documentElement.scrollWidth};})()`);
    assert.equal(layout.button,'button');assert.ok(['status','alert'].includes(layout.role));assert.equal(layout.overflow,false);assert.ok(layout.scroll<=width+1);
    await evaluate(`document.querySelector('#wh-dialog-close').click()`);
    await evaluate(`new Promise(r=>requestAnimationFrame(()=>requestAnimationFrame(r)))`);
  }
  assert.deepEqual(await evaluate('__errors'),[]);
});

test('通知设置：测试待处理时防重复和锁定编辑，关闭与导航隔离迟到结果', {timeout:60000}, async t => {
  if (!available(t)) return;
  const {call,evaluate}=await browser(t);
  await evaluate(`(async()=>{await page('settings');document.querySelector('[data-settings-category="notifications"]').click();for(let i=0;i<100&&document.querySelector('#wh-add').disabled;i++)await new Promise(r=>setTimeout(r,10));document.querySelector('#wh-add').click();if(!document.querySelector('#wh-draft-url'))throw Error('Webhook modal did not open: '+document.querySelector('#wh-status')?.textContent+'; '+__errors.join(';'));})()`);
  for(const value of ['', 'not a URL','ftp://example.invalid']){
    const invalid=await evaluate(`(()=>{document.querySelector('#wh-draft-url').value=${JSON.stringify(value)};const n=__calls.length;document.querySelector('#wh-dialog [data-test]').click();return {calls:__calls.length-n,text:document.querySelector('#wh-dialog [data-test-status]').textContent};})()`);
    assert.equal(invalid.calls,0);assert.ok(invalid.text);
  }
  const modalSent=await evaluate(`(async()=>{document.querySelector('#wh-draft-url').value='https://example.invalid/modal-json';const b=document.querySelector('#wh-dialog [data-test]');b.click();for(let i=0;i<100&&b.disabled;i++)await new Promise(r=>setTimeout(r,10));return {text:document.querySelector('#wh-dialog [data-test-status]').textContent,cards:document.querySelectorAll('[data-target-index]').length,request:__calls.filter(c=>c.url.split('?')[0].endsWith('/webhook/test')).at(-1)};})()`);
  assert.match(modalSent.text,/37/);assert.equal(modalSent.cards,1);assert.deepEqual(JSON.parse(modalSent.request.body),{url:'https://example.invalid/modal-json',format:'json',timeout_seconds:5,allow_private:false});
  const pending=await evaluate(`(()=>{__testDelay=true;document.querySelector('#wh-draft-url').value='https://example.invalid/draft';document.querySelector('#wh-draft-format').value='form';document.querySelector('#wh-draft-timeout').value='13';document.querySelector('#wh-draft-allow-private').checked=true;const b=document.querySelector('#wh-dialog [data-test]');b.click();b.click();document.querySelector('#wh-dialog-form').requestSubmit();return {calls:__calls.filter(c=>c.url.split('?')[0].endsWith('/webhook/test')),main:['wh-save','wh-reload','wh-add'].map(id=>document.getElementById(id).disabled),fields:[...document.querySelectorAll('#wh-dialog-body input,#wh-dialog-body select')].every(e=>e.disabled),close:document.querySelector('#wh-dialog-close').disabled,submit:document.querySelector('#wh-dialog button[type="submit"]').disabled};})()`);
  assert.equal(pending.calls.length,2);assert.deepEqual(JSON.parse(pending.calls.at(-1).body),{url:'https://example.invalid/draft',format:'form',timeout_seconds:13,allow_private:true});
  assert.deepEqual(pending.main,[true,true,true]);assert.equal(pending.fields,true);assert.equal(pending.close,false);assert.equal(pending.submit,true);
  await call('Input.dispatchKeyEvent',{type:'keyDown',key:'Escape',code:'Escape',windowsVirtualKeyCode:27});
  await call('Input.dispatchKeyEvent',{type:'keyUp',key:'Escape',code:'Escape',windowsVirtualKeyCode:27});
  await evaluate(`new Promise(r=>requestAnimationFrame(()=>requestAnimationFrame(r)))`);
  const closed=await evaluate(`({open:document.querySelector('#wh-dialog').open,aborted:__lastTestSignal?.aborted,unlocked:!document.querySelector('#wh-add').disabled})`);
  assert.equal(closed.open,false);assert.equal(closed.aborted,true);assert.equal(closed.unlocked,true);
  await evaluate(`document.querySelector('#wh-add').click();document.querySelector('#wh-draft-url').value='https://example.invalid/fresh'`);
  const clean=await evaluate(`document.querySelector('#wh-dialog [data-test-status]').textContent`);
  await evaluate(`(async()=>{__resolveTest();await new Promise(r=>setTimeout(r,30));})()`);
  assert.equal(await evaluate(`document.querySelector('#wh-dialog [data-test-status]').textContent`),clean,'旧请求不得覆盖新弹窗');
  assert.equal(await evaluate(`document.querySelector('#wh-draft-url').value`),'https://example.invalid/fresh');
  await evaluate(`document.querySelector('#wh-dialog-close').click()`);
  await evaluate(`new Promise(r=>requestAnimationFrame(()=>requestAnimationFrame(r)))`);
  const cardPending=await evaluate(`(()=>{const c=document.querySelector('[data-target-index]');c.open=true;const b=c.querySelector('[data-test]');b.click();b.click();return {disabled:b.disabled,locked:['wh-save','wh-reload','wh-add'].every(id=>document.getElementById(id).disabled),fields:[...c.querySelectorAll('input,select,button')].every(e=>e.disabled)};})()`);
  assert.deepEqual(cardPending,{disabled:true,locked:true,fields:true});
  await evaluate(`page('overview')`);
  assert.equal(await evaluate(`__lastTestSignal.aborted`),true,'导航中止当前测试请求');
  const content=await evaluate(`document.querySelector('#content').innerHTML`);
  await evaluate(`(async()=>{__resolveTest();await new Promise(r=>setTimeout(r,30));})()`);
  assert.equal(await evaluate(`document.querySelector('#content').innerHTML`),content,'导航后迟到结果不能改变新页面');
  assert.equal(await evaluate(`__calls.filter(c=>c.method==='PUT').length`),0);
  assert.equal(await evaluate(`__calls.filter(c=>c.url.split('?')[0].endsWith('/webhook/test')).length`),3,'无自动重试');
  assert.deepEqual(await evaluate('__errors'),[]);
});

test('首页分组：六库紧凑展示、数据表独立边界与服务自然堆叠', { timeout: 90000 }, async t => {
  if (!available(t)) return;
  const { call, evaluate } = await browser(t);
  await call('Emulation.setEmulatedMedia', { features: [{ name:'prefers-reduced-motion', value:'reduce' }] });
  await evaluate(`window.__libraryItems=Array.from({length:6},(_,i)=>({Id:i+1,Name:i===5?'长媒体库名称'+ 'Archive'.repeat(12):'测试媒体库 '+(i+1),count:100+i,Path:'/media/'+i}))`);
  for (const width of [320, 390, 768, 1440, 1728]) {
    await call('Emulation.setDeviceMetricsOverride', { width, height: 960, deviceScaleFactor: 1, mobile: width < 860 });
    await evaluate(`(async()=>{await page('overview');await new Promise(r=>setTimeout(r,250));})()`);
    const geometry = await evaluate(`(()=>{
      const root=document.querySelector('.dashboard'), box=e=>{const r=e.getBoundingClientRect();return {left:r.left,right:r.right,top:r.top,bottom:r.bottom,height:r.height,width:r.width};};
      const cards=[...root.querySelectorAll('.dash-card')], main=root.querySelector('.dash-main'), data=root.querySelector('.dash-data'), list=root.querySelector('.dash-library-list');
      return {root:box(root),scroll:document.documentElement.scrollWidth,first:cards.slice(0,2).map(e=>e.querySelector('h2,h3').textContent),tasks:!!root.querySelector('[data-dash="tasks"]'),libraries:[...list.children].map(e=>({box:box(e),visible:e.getClientRects().length>0})),libraryColumns:getComputedStyle(list).gridTemplateColumns.split(' ').length,mainRow:getComputedStyle(main).gridRowStart,dataParent:data.parentElement===root,data:box(data),caption:{text:data.querySelector('caption').textContent,position:getComputedStyle(data.querySelector('caption')).position,width:box(data.querySelector('caption')).width},groups:[...root.querySelectorAll('.dash-service-stack')].map(e=>({label:e.getAttribute('aria-label'),titles:[...e.querySelectorAll('h3')].map(h=>h.textContent)})),order:[...root.querySelectorAll('h2,h3')].map(e=>e.textContent),columns:getComputedStyle(root.querySelector('.dash-service-cards')).gridTemplateColumns.split(' ').length};
    })()`);
    assert.ok(geometry.scroll <= width + 1, `no page overflow at ${width}`);
    assert.deepEqual(geometry.first, ['媒体库状态', '媒体档案']);
    assert.equal(geometry.tasks, false);
    assert.equal(geometry.libraries.length, 6);
    assert.ok(geometry.libraries.every(row=>row.visible && row.box.left>=geometry.root.left-1 && row.box.right<=geometry.root.right+1));
    assert.equal(geometry.libraryColumns, width > 650 ? 2 : 1);
    assert.equal(geometry.mainRow, 'auto');
    assert.equal(geometry.dataParent, true, 'table disclosure is not inside either chart');
    assert.ok(Math.abs(geometry.data.width-geometry.root.width)<1, 'table disclosure occupies its own full row');
    assert.equal(geometry.caption.text, '所选窗口逐分钟监控样本');
    assert.equal(geometry.caption.position, 'absolute'); assert.ok(geometry.caption.width<=1, 'caption stays accessible without adding visible height');
    assert.deepEqual(geometry.groups, [{label:'基础环境',titles:['程序与环境','存储与缓存']},{label:'作业与服务',titles:['后台作业','服务配置']}]);
    assert.equal(geometry.columns, width > 650 ? 2 : 1);
    assert.ok(geometry.order.indexOf('请求与服务端错误') < geometry.order.indexOf('请求耗时分位数'));
    assert.ok(geometry.order.indexOf('请求耗时分位数') < geometry.order.indexOf('Go 运行时'));
    const opened = await evaluate(`(()=>{
      const root=document.querySelector('.dashboard'), data=root.querySelector('.dash-data'), rect=e=>e.getBoundingClientRect(), charts=[...root.querySelectorAll('.dash-chart')], before=charts.map(e=>rect(e).height);
      data.open=true; const wrap=data.querySelector('.table-wrap'), table=data.querySelector('table'), d=rect(data),w=rect(wrap), caption=data.querySelector('caption');
      return {before,after:charts.map(e=>rect(e).height),inside:w.left>=d.left&&w.right<=d.right&&w.bottom<=d.bottom,margin:getComputedStyle(wrap).margin,rows:table.tBodies[0].rows.length,caption:caption.textContent,scroll:document.documentElement.scrollWidth};
    })()`);
    assert.deepEqual(opened.after, opened.before, 'opening data cannot stretch charts');
    assert.equal(opened.inside,true, `table border remains inside card at ${width}`);
    assert.equal(opened.margin, '0px'); assert.equal(opened.rows,15); assert.ok(opened.scroll<=width+1);
    const stack = await evaluate(`(()=>{
      const groups=[...document.querySelectorAll('.dash-service-stack')], first=groups[0].children[0], next=groups[0].children[1], other=groups[1].children[0], h=e=>e.getBoundingClientRect().height, before=h(other);
      first.querySelector('details').open=true;
      return {gap:next.getBoundingClientRect().top-first.getBoundingClientRect().bottom,otherBefore:before,otherAfter:h(other),height:h(first),scroll:document.documentElement.scrollWidth};
    })()`);
    assert.ok(Math.abs(stack.gap-16)<1, 'expanded card pushes only its own semantic stack');
    assert.equal(stack.otherAfter,stack.otherBefore,'neighbor stack is not stretched'); assert.ok(stack.scroll<=width+1);
    await evaluate(`document.querySelector('.dash-service-stack summary').focus()`);
    const tabOrder=[];
    for (let i=0;i<4;i++) {
      tabOrder.push(await evaluate(`document.activeElement.closest('.dash-service-card')?.getAttribute('aria-label')`));
      if(i<3){await call('Input.dispatchKeyEvent',{type:'keyDown',key:'Tab',code:'Tab',windowsVirtualKeyCode:9});await call('Input.dispatchKeyEvent',{type:'keyUp',key:'Tab',code:'Tab',windowsVirtualKeyCode:9});}
    }
    assert.deepEqual(tabOrder,['程序与环境','存储与缓存','后台作业','服务配置'],'keyboard follows the fixed semantic groups');
    if (process.env.EMBY_LAYOUT_ARTIFACT_DIR) {
      const output=path.resolve(process.env.EMBY_LAYOUT_ARTIFACT_DIR); fs.mkdirSync(output,{recursive:true});
      const shot=await call('Page.captureScreenshot',{format:'png',captureBeyondViewport:true});
      fs.writeFileSync(path.join(output,`overview-groups-${width}.png`),Buffer.from(shot.data,'base64'));
    }
    assert.deepEqual(await evaluate('__errors'),[]);
  }
});

test('资料处理整合：响应分组、范围独立、旧入口兼容与防火墙首卡间距', { timeout: 60000 }, async t => {
  if (!available(t)) return;
  const { call, evaluate } = await browser(t);
  await call('Emulation.setEmulatedMedia', { features: [{ name:'prefers-reduced-motion', value:'reduce' }] });
  await evaluate(`window.__libraryItems=Array.from({length:3},(_,i)=>({Id:i+1,Name:'媒体库 '+(i+1),count:10,Path:'/media/'+i}))`);
  for (const width of [390, 768, 1440]) {
    await call('Emulation.setDeviceMetricsOverride',{width,height:900,deviceScaleFactor:1,mobile:width<860});
    const layout=await evaluate(`(async()=>{await page('translate');await new Promise(r=>setTimeout(r,150));const grid=document.querySelector('.scrape-tools-grid');return {cards:[...grid.querySelectorAll(':scope > .panel,:scope > .scrape-operation-stack > .panel')].map(e=>e.id),columns:getComputedStyle(grid).gridTemplateColumns.split(' ').length,scope:!!document.querySelector('#translate-library'),candidate:document.querySelector('#scrape-count').textContent,scroll:document.documentElement.scrollWidth};})()`);
    assert.equal(layout.scope,true,'legacy translate navigation retains translation controls');
    assert.ok(layout.cards.includes('scrape-run-card')&&layout.cards.includes('avatar-card')&&layout.cards.includes('translation-card'));
    assert.equal(layout.columns,width>850?2:1); assert.match(layout.candidate,/12/); assert.ok(layout.scroll<=width+1);
    const scopes=await evaluate(`(async()=>{
      document.querySelector('#sc-lib').value='1';document.querySelector('#avatar-library').value='2';document.querySelector('#translate-library').value='3';document.querySelector('#avatar-limit').value='9';document.querySelector('#translate-limit').value='5';
      document.querySelector('#avatar-form').requestSubmit();document.querySelector('[data-translate-start]').click();await new Promise(r=>setTimeout(r,50));
      const calls=__calls.filter(c=>c.method==='POST'),last=ending=>JSON.parse(calls.filter(c=>c.url.endsWith(ending)).at(-1).body);
      return {scrape:document.querySelector('#sc-lib').value,avatar:last('/scrape/avatars'),translation:last('/translate/run'),translateState:document.querySelector('[data-translate-progress]').textContent};
    })()`);
    assert.equal(scopes.scrape,'1'); assert.deepEqual(scopes.avatar,{library_id:2,limit:9});
    assert.equal(scopes.translation.library_id,3);assert.equal(scopes.translation.limit,5);assert.deepEqual(scopes.translation.fields,['title']);
    assert.doesNotMatch(scopes.translateState,/状态未知/);
    const firewall=await evaluate(`(async()=>{await page('firewall');await new Promise(r=>setTimeout(r,100));const first=document.querySelector('#firewall-blocklist .panel').getBoundingClientRect(),next=document.querySelector('#firewall-login-title').closest('.panel').getBoundingClientRect();return {gap:next.top-first.bottom,scroll:document.documentElement.scrollWidth};})()`);
    assert.ok(Math.abs(firewall.gap-16)<1,`firewall first-card gap at ${width}: ${firewall.gap}`);assert.ok(firewall.scroll<=width+1);
    assert.deepEqual(await evaluate('__errors'),[]);
  }
});

test('完整前端：桌面与移动端页面布局、渐进表单和触控导航', { timeout: 120000 }, async t => {
  if (!available(t)) return;
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
        const bento = await evaluate(`(()=>{const r=document.querySelector('#overview-dashboard'), cards=[...r.querySelectorAll('.dash-card')], main=getComputedStyle(r.querySelector('.dash-main'));return {columns:getComputedStyle(r).gridTemplateColumns.split(' ').length,grids:[r,...r.querySelectorAll('*')].filter(e=>getComputedStyle(e).display==='grid').length,first:cards[0].querySelector('h2').textContent,backgrounds:[...new Set(cards.map(e=>getComputedStyle(e).backgroundColor))],radii:[...new Set(cards.map(e=>getComputedStyle(e).borderRadius))],row:main.gridRowStart,col:main.gridColumnStart,service:r.querySelectorAll('.dash-service-card').length,scan:r.querySelector('#overview-scan').textContent};})()`);
        assert.equal(bento.columns, width > 1100 ? 4 : width > 650 ? 2 : 1);
        assert.ok(bento.grids >= 5, 'explicit component grids'); assert.equal(bento.first, '媒体库状态');
        assert.deepEqual(bento.backgrounds, ['rgb(21, 21, 23)']); assert.equal(bento.radii.length, 1);
        assert.equal(bento.row, 'auto'); assert.equal(bento.col, width > 650 ? 'span 2' : 'auto');
        assert.equal(bento.service, 4); assert.match(bento.scan, /测试媒体库 · 42 \/ 120/);
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


test('报告回归：预览弹窗、深色选项、雪碧图计划、日志自动更新、审计筛选与IP黑名单', {timeout:120000}, async t => {
  if (!available(t)) return;
  const {call,evaluate}=await browser(t);
  const output=process.env.EMBY_REPORT_ARTIFACT_DIR;
  if(output){assert.ok(path.resolve(output).startsWith('/tmp/'));fs.mkdirSync(output,{recursive:true});}
  async function shot(name){if(!output)return;const result=await call('Page.captureScreenshot',{format:'png'});fs.writeFileSync(path.join(output,name+'.png'),Buffer.from(result.data,'base64'));}
  async function openOptions(selector,name){
    const point=await evaluate(`(()=>{const s=document.querySelector(${JSON.stringify(selector)});s.scrollIntoView({block:'center'});const r=s.getBoundingClientRect();return {x:r.x+r.width/2,y:r.y+r.height/2};})()`);
    await call('Input.dispatchMouseEvent',{type:'mousePressed',button:'left',clickCount:1,...point});
    await call('Input.dispatchMouseEvent',{type:'mouseReleased',button:'left',clickCount:1,...point});
    await shot(name);
    await call('Input.dispatchKeyEvent',{type:'keyDown',key:'Escape',code:'Escape',windowsVirtualKeyCode:27});
    await call('Input.dispatchKeyEvent',{type:'keyUp',key:'Escape',code:'Escape',windowsVirtualKeyCode:27});
  }
  for(const width of [375,768,1280]){
    await call('Emulation.setDeviceMetricsOverride',{width,height:900,deviceScaleFactor:1,mobile:width<860});
    await evaluate("page('libraries')");
    const dialog=await evaluate(`(()=>{const b=document.querySelector('[data-lib-sprites]');b.focus();b.click();const d=document.querySelector('#library-sprite-settings');return {open:d.open,focus:d.contains(document.activeElement),scroll:document.documentElement.scrollWidth};})()`);
    assert.equal(dialog.open,true);assert.equal(dialog.focus,true);assert.ok(dialog.scroll<=width+1);
    await shot('preview-dialog-'+width);
    const colors=await evaluate(`(()=>{const s=document.querySelector('[data-library-sprite="preset"]'),o=s.options[0];return {fg:getComputedStyle(o).color,bg:getComputedStyle(o).backgroundColor,scheme:getComputedStyle(s).colorScheme};})()`);
    assert.equal(colors.bg,'rgb(32, 32, 36)');assert.equal(colors.fg,'rgb(255, 255, 255)');assert.equal(colors.scheme,'dark');
    await openOptions('[data-library-sprite="preset"]','preview-options-'+width);
    await call('Input.dispatchKeyEvent',{type:'keyDown',key:'Escape',code:'Escape',windowsVirtualKeyCode:27});
    await call('Input.dispatchKeyEvent',{type:'keyUp',key:'Escape',code:'Escape',windowsVirtualKeyCode:27});
    assert.equal(await evaluate(`document.activeElement.matches('[data-lib-sprites]')&&!document.querySelector('#library-sprite-settings').open`),true);
    await evaluate("page('scheduled')");
    const sprites=await evaluate(`(()=>{document.querySelector('#sched-form').closest('details').open=true;const s=document.querySelector('#s-type');s.value='sprites';s.dispatchEvent(new Event('change'));return {shown:!document.querySelector('#s-sprites-limit').closest('.field').hidden,text:document.querySelector('#sched-form').innerText,scroll:document.documentElement.scrollWidth};})()`);
    assert.match(sprites.text,/雪碧图/);assert.ok(sprites.scroll<=width+1);await shot('scheduled-sprites-'+width);
    await openOptions('#s-type','scheduled-options-'+width);
    await evaluate(`(()=>{const s=document.querySelector('#s-type');s.value='scan';s.dispatchEvent(new Event('change'));})()`);
    await openOptions('#s-scan-mode','scan-options-'+width);
    await evaluate("page('firewall')");await evaluate(`(async()=>{for(let i=0;i<30&&!document.querySelector('[data-security="client"]').textContent;i++)await new Promise(r=>setTimeout(r,10));})()`);
    await shot('security-'+width);assert.ok(await evaluate('document.documentElement.scrollWidth')<=width+1);
  }
  const security=await evaluate(`(async()=>{const q=k=>document.querySelector('[data-security="'+k+'"]');q('network').value='203.0.113.5';q('note').value='<img src=x onerror=alert(1)>';q('form').requestSubmit();await new Promise(r=>setTimeout(r,40));const result={row:q('rules').innerText,xss:!!q('rules').querySelector('img'),calls:__calls.filter(c=>c.method==='POST'&&c.url.includes('ip-blocklist')).length};q('rules').querySelector('button').click();await new Promise(r=>setTimeout(r,40));result.deleted=!q('rules').querySelector('button');result.body=JSON.parse(__calls.findLast(c=>c.method==='DELETE').body);return result;})()`);
  assert.match(security.row,/<img/);assert.equal(security.xss,false);assert.equal(security.calls,1);assert.equal(security.deleted,true);assert.deepEqual(security.body,{network:'203.0.113.5'});
  const denial=await evaluate(`(async()=>{__securityFail=true;const q=k=>document.querySelector('[data-security="'+k+'"]');q('network').value='127.0.0.1';q('form').requestSubmit();await new Promise(r=>setTimeout(r,40));return {error:q('error').textContent,draft:q('network').value,disabled:q('add').disabled};})()`);
  assert.match(denial.error,/误封/);assert.doesNotMatch(denial.error,/<img/);assert.equal(denial.draft,'127.0.0.1');assert.equal(denial.disabled,false);
  await evaluate("page('firewall')");
  const audit=await evaluate(`(async()=>{document.querySelector('#firewall-login-ip').value='203.0.113.5';document.querySelector('#firewall-login-result').value='failed';document.querySelector('#firewall-login-filter').requestSubmit();await new Promise(r=>setTimeout(r,40));return {url:__calls.findLast(c=>c.url.includes('login-events')).url,xss:!!document.querySelector('#content img')};})()`);
  assert.match(audit.url,/ip=203.0.113.5/);assert.match(audit.url,/result=failed/);assert.equal(audit.xss,false);await shot('firewall-filter-1280');
  assert.equal(await evaluate(`document.querySelectorAll('.nav-item[data-page="probe"],.nav-item[data-page="security"],.nav-item[data-page="activity"]').length`),0,'旧诊断/审计导航已移除，登录记录与黑名单统一在防火墙');
  await evaluate("page('logs')");assert.equal(await evaluate("!!document.querySelector('#dl-newbar')"),false);
  const before=await evaluate("__logID");await evaluate("new Promise(r=>setTimeout(r,1400))");assert.ok(await evaluate('__logID')>before);
  const paused=await evaluate(`(()=>{document.querySelector('#dl-pause').click();return __logID;})()`);await evaluate('new Promise(r=>setTimeout(r,1300))');assert.equal(await evaluate('__logID'),paused);
  await evaluate("document.querySelector('#dl-pause').click()");await evaluate('new Promise(r=>setTimeout(r,1400))');assert.ok(await evaluate('__logID')>paused);await shot('logs-auto-1280');
  const errors=await evaluate('__errors');assert.deepEqual(errors,[]);
});

test('详情固定外壳：加载关闭、错误重试、长详情关闭、背景隔离、持久反馈与焦点归还', {timeout:60000}, async t => {
  if (!available(t)) return;
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
});

test('实心强调控件：详情保存与媒体库添加在各视口及交互状态保持4.5文字对比度', {timeout:60000}, async t => {
  if (!available(t)) return;
  const {call,evaluate}=await browser(t);
  await call('DOM.enable');
  await call('CSS.enable');
  const luminance=rgb=>rgb.map(c=>c/255).map(c=>c<=.04045?c/12.92:((c+.055)/1.055)**2.4).reduce((sum,c,i)=>sum+c*[.2126,.7152,.0722][i],0);
  for (const viewport of [{width:375,height:812,scale:1},{width:768,height:1024,scale:1},{width:1280,height:900,scale:1},{width:640,height:450,scale:2}]) {
    // 640 CSS px at DPR 2 exercises the layout equivalent of 1280px at 200%, not browser-menu zoom.
    await call('Emulation.setDeviceMetricsOverride',{width:viewport.width,height:viewport.height,deviceScaleFactor:viewport.scale,mobile:false});
    await evaluate("page('libraries')");
    for (const selector of ['[data-lib-add]','#detail-edit-save']) {
      if (selector==='#detail-edit-save') await evaluate(`(async()=>{await openDetail({id:1});document.querySelector('.detail-maintenance').open=true;document.querySelector('#detail-edit-toggle').click();})()`);
      const {root}=await call('DOM.getDocument');
      const {nodeId}=await call('DOM.querySelector',{nodeId:root.nodeId,selector});
      assert.ok(nodeId,selector);
      for (const forcedPseudoClasses of [[],['hover'],['focus','focus-visible'],['hover','active']]) {
        // Force Chromium's real CSS pseudo states without submitting either form.
        await call('CSS.forcePseudoState',{nodeId,forcedPseudoClasses});
        const colors=await evaluate(`(async()=>{const b=document.querySelector(${JSON.stringify(selector)});b.scrollIntoView({block:'center'});await new Promise(requestAnimationFrame);await Promise.all(b.getAnimations().map(a=>a.finished));const s=getComputedStyle(b),text=getComputedStyle(b.querySelector('span'));return {fg:text.color,bg:s.backgroundColor,backgroundImage:s.backgroundImage,opacity:s.opacity,visible:b.getBoundingClientRect().width>0};})()`);
        const label=JSON.stringify({viewport,selector,state:forcedPseudoClasses,colors});
        assert.equal(colors.visible,true,label);
        assert.equal(colors.opacity,'1',label);
        assert.equal(colors.backgroundImage,'none',label);
        // These controls have opaque fills and opaque text. Do not silently discard
        // rgba alpha and compare against black if their composition ever changes.
        const fg=colors.fg.match(/[\d.]+/g).map(Number),bg=colors.bg.match(/[\d.]+/g).map(Number);
        assert.equal(fg[3]??1,1,label);
        assert.equal(bg[3]??1,1,label);
        const a=luminance(fg.slice(0,3)),b=luminance(bg.slice(0,3)),ratio=(Math.max(a,b)+.05)/(Math.min(a,b)+.05);
        assert.ok(ratio>=4.5,`${label}; contrast=${ratio}`);
      }
      await call('CSS.forcePseudoState',{nodeId,forcedPseudoClasses:[]});
    }
    await evaluate('closeDetail()');
    assert.equal(await evaluate('document.documentElement.scrollWidth<=innerWidth'),true,JSON.stringify(viewport));
  }
});

test('扫描原生progress：确定/未知/受限/错误与长内容窄屏、键盘收起', {timeout:60000}, async t => {
  if (!available(t)) return;
  const {call,evaluate}=await browser(t);
  await evaluate('(async()=>{await new Promise(requestAnimationFrame);stopScanPolling();stopDashboardPolling()})()');
  for (const width of [320,390,768,1440]) {
    await call('Emulation.setDeviceMetricsOverride',{width,height:800,deviceScaleFactor:1,mobile:false});
    const result=await evaluate(`(()=>{
      renderScanProgress({running:true,mode:'incremental',library_name:'长媒体库名称/'.repeat(12),groups_total:120,groups_done:42,current:'/media/'+'long-file-name'.repeat(20)+'.strm'});
      const p=document.querySelector('#scan-progress-fill'),panel=document.querySelector('#scan-progress');
      const r=panel.getBoundingClientRect();
      const known={tag:p.tagName,max:p.max,value:p.value,indeterminate:p.matches(':indeterminate'),label:p.getAttribute('aria-labelledby'),close:scanClose.hidden,closeVisible:!!scanClose.getClientRects().length,fits:r.left>=0&&r.right<=innerWidth&&r.top>=0&&r.bottom<=innerHeight,overflow:panel.scrollWidth>panel.clientWidth,live:panel.hasAttribute('aria-live')};
      renderScanProgress({running:true,total:0,done:7});
      const unknown={indeterminate:p.matches(':indeterminate'),count:scanCount.textContent};
      scanProgressUnavailable(3000);
      const unavailable={hidden:p.hidden,value:p.hasAttribute('value'),title:scanTitle.textContent};
      renderScanProgress({running:false,finished_at:'now',groups_total:120,groups_done:42,mode:'incremental',failed:2,incomplete:true,error:'失败的长文件路径/'.repeat(120)});
      const error={state:panel.dataset.state,value:p.value,close:scanClose.hidden,detail:scanDetail.textContent,scrollable:scanDetail.scrollHeight>scanDetail.clientHeight,buttonHeight:scanClose.getBoundingClientRect().height};
      scanClose.focus();
      return {known,unknown,unavailable,error};
    })()`);
    assert.deepEqual(result.known,{tag:'PROGRESS',max:100,value:35,indeterminate:false,label:'scan-progress-title',close:true,closeVisible:false,fits:true,overflow:false,live:false},String(width));
    assert.equal(result.unknown.indeterminate,true); assert.match(result.unknown.count,/7.*总量待确认/);
    assert.deepEqual(result.unavailable,{hidden:true,value:false,title:'扫描状态未确认'});
    assert.equal(result.error.state,'error'); assert.equal(result.error.value,35); assert.equal(result.error.close,false);
    assert.match(result.error.detail,/未执行缺失索引清理/); assert.equal(result.error.scrollable,true); assert.ok(result.error.buttonHeight>=44);
    assert.equal(await evaluate('document.activeElement.id'),'scan-progress-close','result dismiss remains keyboard focusable');
    await call('Input.dispatchKeyEvent',{type:'keyDown',key:'Enter',code:'Enter',windowsVirtualKeyCode:13,text:'\r'});
    await call('Input.dispatchKeyEvent',{type:'keyUp',key:'Enter',code:'Enter',windowsVirtualKeyCode:13});
    assert.equal(await evaluate('scanPanel.hidden'),true);
    assert.equal(await evaluate('__calls.some(c=>c.method==="POST")'),false,'dismissing results never cancels or resubmits a scan');
    await evaluate('renderScanProgress({running:true,library_name:"电影与剧集",mode:"incremental",groups_total:120,groups_done:42,unchanged_groups:30,added:8,updated:4,current:"/media/示例影片.strm"})');
    if(process.env.SCAN_PROGRESS_SCREENSHOT_DIR && [390,1440].includes(width)) {
      fs.mkdirSync(process.env.SCAN_PROGRESS_SCREENSHOT_DIR,{recursive:true});
      const image=await call('Page.captureScreenshot',{format:'png',captureBeyondViewport:false});
      fs.writeFileSync(path.join(process.env.SCAN_PROGRESS_SCREENSHOT_DIR,`scan-${width}.png`),Buffer.from(image.data,'base64'));
      if(width===1440) {
        const clip=await evaluate('(()=>{const r=scanPanel.getBoundingClientRect();return {x:r.x+scrollX,y:r.y+scrollY,width:r.width,height:r.height,scale:1}})()');
        const control=await call('Page.captureScreenshot',{format:'png',clip});
        fs.writeFileSync(path.join(process.env.SCAN_PROGRESS_SCREENSHOT_DIR,'scan-control.png'),Buffer.from(control.data,'base64'));
      }
    }
  }
  await call('Emulation.setEmulatedMedia',{features:[{name:'prefers-reduced-motion',value:'reduce'}]});
  assert.equal(await evaluate(`(()=>{renderScanProgress({running:true});return getComputedStyle(scanFill,'::-webkit-progress-bar').animationName;})()`),'none');
});


test('原生选项与焦点：鼠标/Tab/Space/单选方向键、禁用、长标签和高对比度', {timeout:60000}, async t => {
  if (!available(t)) return;
  const {call,evaluate}=await browser(t);
  await evaluate(`(async()=>{await page("scheduled");stopScanPolling();stopDashboardPolling();await Promise.all(document.querySelector('#content').getAnimations({subtree:true}).filter(a=>a.effect.getComputedTiming().endTime!==Infinity).map(a=>a.finished))})()`);
  assert.deepEqual(await evaluate(`(()=>{const s=document.querySelector('.switch input');return {type:s.type,role:s.role,name:s.getAttribute('aria-label'),visible:s.getBoundingClientRect().width>=44}})()`),{type:'checkbox',role:'switch',name:'启用计划任务：夜间扫描',visible:true});
  await evaluate(`(()=>{window.__toggleDelay=true;document.querySelector('.switch input').focus()})()`);
  for(const type of ['keyDown','keyUp'])await call('Input.dispatchKeyEvent',{type,key:' ',code:'Space',windowsVirtualKeyCode:32});
  assert.deepEqual(await evaluate(`({disabled:document.querySelector('.switch input').disabled,busy:document.querySelector('.switch input').getAttribute('aria-busy'),posts:__calls.filter(c=>c.method==='POST'&&c.url.endsWith('/toggle')).length})`),{disabled:true,busy:'true',posts:1});
  for(const type of ['keyDown','keyUp'])await call('Input.dispatchKeyEvent',{type,key:' ',code:'Space',windowsVirtualKeyCode:32});
  assert.equal(await evaluate(`__calls.filter(c=>c.method==='POST'&&c.url.endsWith('/toggle')).length`),1);
  await evaluate(`(async()=>{__resolveToggle();for(let i=0;i<100;i++){const e=document.querySelector('.switch input');if(e&&!e.disabled&&!e.checked&&document.activeElement===e)return;await new Promise(r=>setTimeout(r,10));}throw Error('switch did not restore focus/state')})()`);
  await evaluate(`(()=>{window.__toggleFail=true;document.querySelector('.switch input').focus()})()`);
  for(const type of ['keyDown','keyUp'])await call('Input.dispatchKeyEvent',{type,key:' ',code:'Space',windowsVirtualKeyCode:32});
  await evaluate(`(async()=>{__resolveToggle();for(let i=0;i<100;i++){const e=document.querySelector('.switch input');if(e&&!e.disabled&&!e.checked&&document.activeElement===e)return;await new Promise(r=>setTimeout(r,10));}throw Error('failed switch did not restore focus/state')})()`);
  // Production has no radio group: exercise the shared styles with real native radios,
  // not a new business setting or a hand-written keyboard/event implementation.
  await evaluate(`(()=>{document.querySelector('#content').innerHTML='<section class="panel" id="option-fixture"><h2>键盘焦点与原生选项</h2><button id="focus-start" class="btn">开始</button><label class="check"><input id="native-check" type="checkbox"><span>'+ '很长的复选框标签LongLabel/'.repeat(18) +'</span></label><label class="check"><input id="disabled-check" type="checkbox" disabled>不可用的复选框</label><fieldset><legend>互斥选项</legend><label class="check"><input id="radio-a" type="radio" name="native-group" checked>选项一</label><label class="check"><input id="radio-b" type="radio" name="native-group">选项二</label><label class="check"><input type="radio" name="native-group" disabled>禁用选项</label></fieldset><label class="switch"><input id="native-switch" type="checkbox" role="switch" aria-label="测试即时开关"></label><label class="check"><input id="mixed-check" type="checkbox">部分选中（原生 indeterminate）</label><button class="btn btn-accent" disabled>不可用的按钮</button><p><a href="#fixture" id="focus-link">普通链接</a></p><details><summary>展开更多内容</summary>详情</details></section>';document.querySelector('#mixed-check').indeterminate=true;})()`);
  await evaluate(`Promise.all(document.querySelector('#content').getAnimations({subtree:true}).filter(a=>a.effect.getComputedTiming().endTime!==Infinity).map(a=>a.finished))`);
  for(const width of [320,390,1440]) {
    await call('Emulation.setDeviceMetricsOverride',{width,height:860,deviceScaleFactor:1,mobile:false});
    await evaluate(`(()=>{document.querySelector('#native-check').checked=false;document.querySelector('#radio-a').checked=true;document.querySelector('#native-switch').disabled=false;document.querySelector('#native-switch').checked=false;document.querySelector('#focus-start').scrollIntoView({block:'center'})})()`);
    const point=await evaluate(`(()=>{const r=document.querySelector('#focus-start').getBoundingClientRect();return {x:r.x+r.width/2,y:r.y+r.height/2}})()`);
    await call('Input.dispatchMouseEvent',{type:'mousePressed',button:'left',clickCount:1,...point});
    await call('Input.dispatchMouseEvent',{type:'mouseReleased',button:'left',clickCount:1,...point});
    assert.equal(await evaluate(`document.querySelector('#focus-start').matches(':focus-visible')`),false,'pointer button has no keyboard ring');
    await call('Input.dispatchKeyEvent',{type:'keyDown',key:'Tab',code:'Tab',windowsVirtualKeyCode:9});
    await call('Input.dispatchKeyEvent',{type:'keyUp',key:'Tab',code:'Tab',windowsVirtualKeyCode:9});
    assert.deepEqual(await evaluate(`(()=>{const e=document.activeElement,s=getComputedStyle(e);return {id:e.id,ring:s.outlineStyle,width:s.outlineWidth,visible:e.matches(':focus-visible'),shadow:s.boxShadow}})()`),{id:'native-check',ring:'solid',width:'2px',visible:true,shadow:'none'});
    await call('Input.dispatchKeyEvent',{type:'keyDown',key:' ',code:'Space',windowsVirtualKeyCode:32});
    await call('Input.dispatchKeyEvent',{type:'keyUp',key:' ',code:'Space',windowsVirtualKeyCode:32});
    assert.equal(await evaluate(`document.querySelector('#native-check').checked`),true);
    assert.equal(await evaluate(`(()=>{const e=document.querySelector('#disabled-check');e.closest('label').click();return e.checked})()`),false);
    await call('Input.dispatchKeyEvent',{type:'keyDown',key:'Tab',code:'Tab',windowsVirtualKeyCode:9});
    await call('Input.dispatchKeyEvent',{type:'keyUp',key:'Tab',code:'Tab',windowsVirtualKeyCode:9});
    assert.equal(await evaluate('document.activeElement.id'),'radio-a','disabled checkbox skipped');
    await call('Input.dispatchKeyEvent',{type:'keyDown',key:'ArrowRight',code:'ArrowRight',windowsVirtualKeyCode:39});
    await call('Input.dispatchKeyEvent',{type:'keyUp',key:'ArrowRight',code:'ArrowRight',windowsVirtualKeyCode:39});
    assert.deepEqual(await evaluate(`({id:document.activeElement.id,a:document.querySelector('#radio-a').checked,b:document.querySelector('#radio-b').checked})`),{id:'radio-b',a:false,b:true});
    await call('Input.dispatchKeyEvent',{type:'keyDown',key:'Tab',code:'Tab',windowsVirtualKeyCode:9});
    await call('Input.dispatchKeyEvent',{type:'keyUp',key:'Tab',code:'Tab',windowsVirtualKeyCode:9});
    assert.equal(await evaluate('document.activeElement.id'),'native-switch');
    await call('Input.dispatchKeyEvent',{type:'keyDown',key:' ',code:'Space',windowsVirtualKeyCode:32});
    await call('Input.dispatchKeyEvent',{type:'keyUp',key:' ',code:'Space',windowsVirtualKeyCode:32});
    assert.equal(await evaluate(`document.querySelector('#native-switch').checked`),true);
    const layout=await evaluate(`(()=>{const s=document.querySelector('#native-switch'),c=document.querySelector('#native-check'),label=c.closest('label');return {fits:document.documentElement.scrollWidth<=innerWidth,controlWidth:c.getBoundingClientRect().width,labelHeight:label.getBoundingClientRect().height,switchWidth:s.getBoundingClientRect().width,switchOpacity:getComputedStyle(s).opacity,mixed:document.querySelector('#mixed-check').indeterminate};})()`);
    assert.equal(layout.fits,true,String(width));assert.ok(layout.controlWidth>=18);assert.ok(layout.labelHeight>=44);assert.ok(layout.switchWidth>=44);assert.equal(layout.switchOpacity,'1');assert.equal(layout.mixed,true);
    assert.equal(await evaluate(`(()=>{const e=document.querySelector('#native-switch');e.disabled=true;e.click();return e.checked})()`),true,'disabled switch cannot toggle');
  }
  await call('Emulation.setEmulatedMedia',{features:[{name:'prefers-reduced-motion',value:'reduce'},{name:'forced-colors',value:'active'}]});
  assert.deepEqual(await evaluate(`(()=>{const s=getComputedStyle(document.querySelector('#native-switch'));return {appearance:s.appearance,duration:s.transitionDuration}})()`),{appearance:'auto',duration:'0s'});
  if(process.env.FOCUS_CONTROLS_SCREENSHOT_DIR) {
    await call('Emulation.setEmulatedMedia',{features:[]});
    fs.mkdirSync(process.env.FOCUS_CONTROLS_SCREENSHOT_DIR,{recursive:true});
    await evaluate(`(()=>{document.querySelector('#option-fixture').style.maxWidth='560px';document.querySelector('#native-check').nextElementSibling.textContent='完整保留的长标签，会根据可用空间换行；复选框不压缩，整行都可点击。';const s=document.querySelector('#native-switch');s.disabled=false;s.closest('label').append('即时开关');document.querySelector('#native-check').focus();})()`);
    for(const width of [390,1440]) {
      await call('Emulation.setDeviceMetricsOverride',{width,height:1000,deviceScaleFactor:1,mobile:false});
      await evaluate(`(async()=>{scrollTo({top:0,left:0,behavior:'instant'});await Promise.all(document.getAnimations().filter(a=>a.effect.getComputedTiming().endTime!==Infinity).map(a=>a.finished))})()`);
      const clip=await evaluate(`(()=>{const r=document.querySelector('#option-fixture').getBoundingClientRect();return {x:r.x+scrollX,y:r.y+scrollY,width:r.width,height:r.height,scale:1}})()`);
      const image=await call('Page.captureScreenshot',{format:'png',clip,captureBeyondViewport:true});
      fs.writeFileSync(path.join(process.env.FOCUS_CONTROLS_SCREENSHOT_DIR,`controls-${width}.png`),Buffer.from(image.data,'base64'));
    }
  }
});


test('任务徽标：succeeded与success样式一致，窄列进行中不拆行', {timeout:60000}, async t => {
  if (!available(t)) return;
  const {call,evaluate}=await browser(t);
  await evaluate(`(async()=>{window.__taskItems=[{id:3,type:'scan',status:'succeeded',result:{added:52,updated:27}},{id:2,type:'translate',status:'success'},{id:1,type:'probe',status:'running'}];await page('tasks');stopTasksPolling();stopScanPolling();stopDashboardPolling();await Promise.all(document.getAnimations().filter(a=>a.effect.getComputedTiming().endTime!==Infinity).map(a=>a.finished))})()`);
  for(const width of [390,827,1440]) {
    await call('Emulation.setDeviceMetricsOverride',{width,height:800,deviceScaleFactor:1,mobile:false});
    const result=await evaluate(`(()=>{const nodes=[...document.querySelectorAll('#task-table .badge')];const style=e=>{const s=getComputedStyle(e),d=getComputedStyle(e,'::before');return {color:s.color,background:s.backgroundColor,border:s.borderColor,radius:s.borderRadius,font:s.font,padding:s.padding,dot:d.color,dotWidth:d.width,dotShrink:d.flexShrink}};const r=document.createRange();r.selectNodeContents(nodes[2]);return {scan:style(nodes[0]),other:style(nodes[1]),labels:nodes.map(e=>e.textContent),nowrap:getComputedStyle(nodes[2]).whiteSpace,lines:r.getClientRects().length,fits:document.documentElement.scrollWidth<=innerWidth}})()`);
    assert.deepEqual(result.scan,result.other,String(width));
    assert.notEqual(result.scan.background,'rgba(0, 0, 0, 0)');
    assert.equal(result.scan.dotShrink,'0');
    assert.deepEqual(result.labels,['成功','成功','进行中']);
    assert.equal(result.nowrap,'nowrap');assert.equal(result.lines,1);assert.equal(result.fits,true);
    if(width===827&&process.env.TASK_BADGE_SCREENSHOT) {
      await evaluate(`(async()=>{scrollTo({top:0,left:0,behavior:'instant'});await Promise.all(document.getAnimations().filter(a=>a.effect.getComputedTiming().endTime!==Infinity).map(a=>a.finished))})()`);
      const image=await call('Page.captureScreenshot',{format:'png',captureBeyondViewport:false});
      fs.writeFileSync(process.env.TASK_BADGE_SCREENSHOT,Buffer.from(image.data,'base64'));
    }
  }
});
