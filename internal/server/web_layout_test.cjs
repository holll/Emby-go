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
      if(p==='/api/admin/logs/stream'){
        let timer,closed=false;
        const kind=new URL(url,location.origin).searchParams.get('kind');
        const record={kind,url,token:options.headers['X-Emby-Token'],aborted:false};
        (window.__logStreams ||= []).push(record);
        const stream=new ReadableStream({start(controller){
          const emit=()=>{const id=++__logID;controller.enqueue(new TextEncoder().encode('event: logs\\ndata: '+JSON.stringify({kind,text:window.__logRawText??(kind+'-auto-log-'+id+'\\n'),cursor:String(id),reset:false,gap:false,has_more:false})+'\\n\\n'));};
          const close=()=>{clearInterval(timer);if(!closed){closed=true;controller.close();}};
          record.close=close;
          emit();timer=setInterval(emit,300);
          options.signal?.addEventListener('abort',()=>{record.aborted=true;close();},{once:true});
        },cancel(){clearInterval(timer);closed=true;}});
        return new Response(stream,{headers:{'Content-Type':'text/event-stream'}});
      }
      let data={};
      if(p.endsWith('/Users/Me'))data={Id:'1',Name:'owner',Policy:{IsAdministrator:true}};
      else if(p==='/emby/Sessions')data=window.__sessions||[];
      else if(p==='/emby/Users/1/Views'){const items=window.__nativeViews||[{Id:'1000000000001',Name:'测试媒体库',CollectionType:'movies'}];data={Items:items,TotalRecordCount:items.length};}
      else if(p==='/emby/Users/1/Items'){
        const query=new URL(url,location.origin).searchParams;
        if(query.get('IncludeItemTypes')==='BoxSet'){
          let items=[...(window.__collectionItems||[])];
          const term=(query.get('SearchTerm')||'').toLowerCase();
          items=items.filter(item=>item.Name.toLowerCase().includes(term)).sort((a,b)=>a.Name.localeCompare(b.Name));
          if(query.get('SortOrder')==='Descending')items.reverse();
          const start=Number(query.get('StartIndex')||0),limit=Number(query.get('Limit')||48);
          data={Items:items.slice(start,start+limit),TotalRecordCount:items.length,StartIndex:start};
        }else data={Items:[{Id:'1',Name:'示例影片：移动端资料管理',ProductionYear:2026,UserData:{}},{Id:'2',Name:'另一部影片',UserData:{}}],TotalRecordCount:2};
      }
      else if(decodeURIComponent(p).startsWith('/emby/Users/1/Items/boxset:')){
        const id=decodeURIComponent(p).slice('/emby/Users/1/Items/'.length);
        data=(window.__collectionItems||[]).find(item=>item.Id===id)||{Id:id,Name:'合集',Type:'BoxSet',IsFolder:true,ChildCount:2};
      }
      else if(p==='/emby/Users/1/Items/1/Collections')data={Items:[],TotalRecordCount:0};
      else if(p==='/emby/Users/1/Items/1'){
        if(window.__detailDelay)await new Promise(r=>window.__resolveDetail=r);
        if(window.__detailFail)return {ok:false,status:503,json:async()=>({error:'详情暂不可用，请重试'})};
        data={Id:'1',Name:'长详情验证',Overview:'长简介与文件路径 '.repeat(800),People:[],MediaSources:[],UserData:{}};
      }
      else if(p.endsWith('/libraries')){const items=window.__libraryItems||[{Id:1,Name:'测试媒体库',Path:'/media/'+ 'long-path/'.repeat(12),count:1}];data={total:items.length,items};}
      else if(p.endsWith('/items'))data={total:2,items:[{id:1,status:'success',Title:'示例影片：移动端资料管理',Number:'TEST-001',source_path:'/media/测试/TEST-001.strm'},{id:2,status:'pending',Title:'等待处理的影片',source_path:'/media/待处理/TEST-002.strm'}]};
      else if(p.endsWith('/detail')){
        if(window.__detailDelay)await new Promise(r=>window.__resolveDetail=r);
        if(window.__detailFail)return {ok:false,status:503,json:async()=>({error:'详情暂不可用，请重试'})};
        data={movie:{id:1,Title:'长详情验证',Status:'success',Plot:'长简介与文件路径 '.repeat(800)},metatube_id:{saved:'',canonical:'',valid:false},files:window.__detailFiles||[],actors:window.__detailActors||[],backdrops:window.__detailBackdrops||[]};
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
      else if(p.endsWith('/scheduled'))data=window.__scheduledItems?structuredClone(window.__scheduledItems):{items:[{id:1,name:'夜间扫描',type:'scan',cron:'0 3 * * *',params:'{}',enabled:window.__scheduledEnabled??true,config_etag:'fixture'}],types:{scan:'扫描媒体库',reindex:'重建索引',probe:'媒体信息探测',scrape:'内置刮削',scrape_avatars:'演员头像补全',translate:'独立翻译',sprites:'雪碧图生成'}};
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
      else if(p.endsWith('/playback-events'))data={total:(window.__playbackEvents||[]).length,items:window.__playbackEvents||[]};
      else if(p.endsWith('/probe'))data={items:[{method:'GET',path:'/Items/123/[redacted:123456789abc]',created_at:'2026-10-29'}]};
      else if(p.endsWith('/logs/disk')){const id=++__logID;data={items:[{id:String(id),time:'2026-10-29T12:00:00Z',level:'info',message:'auto-log-'+id}],next_after:String(id),next_before:'',has_more:false};}
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
    assert.deepEqual(geometry.first, ['媒体库状态', '正在播放']);
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

test('概览正在播放：会话卡显示用户与进度，点开进详情，无会话时给出空状态', { timeout: 60000 }, async t => {
  if (!available(t)) return;
  const { call, evaluate } = await browser(t);
  await call('Emulation.setEmulatedMedia', { features: [{ name:'prefers-reduced-motion', value:'reduce' }] });
  await call('Emulation.setDeviceMetricsOverride', { width: 1440, height: 900, deviceScaleFactor: 1, mobile: false });
  const empty = await evaluate(`(async()=>{await page('overview');await new Promise(r=>setTimeout(r,150));return {text:document.querySelector('#overview-sessions').textContent.trim(),count:document.querySelector('[data-dash="sessions-count"]').textContent};})()`);
  assert.match(empty.text, /当前没有正在播放的会话/);
  assert.equal(empty.count, '0 个会话');
  await evaluate(`window.__sessions=[{Id:'s1',UserName:'alice',PlayState:{PositionTicks:250000000},NowPlayingItem:{Id:'1',Name:'长详情验证',RunTimeTicks:1000000000,ImageTags:{Primary:'tag1'},UserData:{}}}]`);
  const card = await evaluate(`(async()=>{await page('overview');for(let i=0;i<100&&!document.querySelector('[data-session-item]');i++)await new Promise(r=>setTimeout(r,20));
    const button=document.querySelector('[data-session-item]'), poster=button.querySelector('img'), bar=button.querySelector('.dash-session-progress i');
    return {count:document.querySelector('[data-dash="sessions-count"]').textContent, title:button.querySelector('strong').textContent,
      detail:button.querySelector('small').textContent, width:bar.style.width, poster:!!poster, item:button.dataset.sessionItem};})()`);
  assert.equal(card.count, '1 个会话');
  assert.equal(card.title, '长详情验证');
  assert.match(card.detail, /alice · 25% · \d\d:\d\d:\d\d/);
  assert.equal(card.width, '25%');
  assert.equal(card.poster, true, '有 Primary 标签时显示海报');
  assert.equal(card.item, '1');
  const opened = await evaluate(`(async()=>{document.querySelector('[data-session-item]').click();await new Promise(r=>setTimeout(r,150));const open=document.querySelector('#detail-overlay').open;document.querySelector('#detail-play').click();await new Promise(r=>setTimeout(r,150));const stacked=document.querySelector('#player-overlay').open&&document.querySelector('#detail-overlay').open;closePlayer();await new Promise(r=>setTimeout(r,80));const drawer=document.querySelector('#detail-overlay').open;closeDetail();return {open,stacked,drawer};})()`);
  assert.equal(opened.open, true, '会话卡可进详情');
  assert.equal(opened.stacked, true, '播放器叠在详情抽屉之上');
  assert.equal(opened.drawer, true, '关闭播放器后详情仍在');
  assert.deepEqual(await evaluate('__errors'), []);
});

test('媒体墙排序方向：与排序字段分开、URL 回显、刷新后保留', { timeout: 60000 }, async t => {
  if (!available(t)) return;
  const { evaluate } = await browser(t);
  await evaluate("page('items')");
  const initial = await evaluate(`(()=>{const s=new URLSearchParams(location.search);return {sort:s.get('sort'),order:s.get('order'),label:document.querySelector('#item-order').textContent};})()`);
  assert.equal(initial.sort, null, '默认排序不写进 URL'); assert.equal(initial.order, null); assert.equal(initial.label, '↓ 降序');
  const toggled = await evaluate(`(async()=>{document.querySelector('#item-order').click();await new Promise(r=>setTimeout(r,200));const s=new URLSearchParams(location.search);return {sort:s.get('sort'),order:s.get('order'),label:document.querySelector('#item-order').textContent,sortValue:document.querySelector('#item-sort').value};})()`);
  assert.equal(toggled.order, 'asc'); assert.equal(toggled.label, '↑ 升序');
  assert.equal(toggled.sort, null, '切方向不改排序字段'); assert.equal(toggled.sortValue, 'datecreated');
  const persisted = await evaluate(`(async()=>{await page('libraries');await page('items');for(let i=0;i<100&&!document.querySelector('#item-order');i++)await new Promise(r=>setTimeout(r,20));return document.querySelector('#item-order').textContent;})()`);
  assert.equal(persisted, '↑ 升序', 'URL 里的方向在重新进入页面后回显');
  const fromScratch = await evaluate(`(async()=>{history.replaceState(null,'',location.pathname+'?sort=year#items');await page('items');for(let i=0;i<100&&!document.querySelector('#item-order');i++)await new Promise(r=>setTimeout(r,20));return document.querySelector('#item-order').textContent;})()`);
  assert.equal(fromScratch, '↓ 降序', '年份等字段默认降序');
  assert.deepEqual(await evaluate('__errors'), []);
});

test('卡片更多菜单：按钮与右键共用、键盘可开合、动作走既有入口', { timeout: 60000 }, async t => {
  if (!available(t)) return;
  const { evaluate } = await browser(t);
  await evaluate(`(async()=>{await page('items');for(let i=0;i<100&&!document.querySelector('.wall-card [data-more]');i++)await new Promise(r=>setTimeout(r,20));})()`);
  const opened = await evaluate(`(()=>{const b=document.querySelector('.wall-card [data-more]');b.focus();b.click();const menu=document.querySelector('#card-menu'),rect=menu.getBoundingClientRect();return {open:!!menu,expanded:b.getAttribute('aria-expanded'),role:menu.getAttribute('role'),items:[...menu.querySelectorAll('[role="menuitem"]')].map(i=>i.textContent.trim()),focused:document.activeElement?.dataset.cardAction,left:rect.left,right:rect.right,viewport:innerWidth};})()`);
  assert.equal(opened.open, true); assert.equal(opened.expanded, 'true'); assert.equal(opened.role, 'menu');
  assert.deepEqual(opened.items, ['打开详情', '播放', '探测媒体信息', '重读源', '删除索引']);
  assert.equal(opened.focused, 'detail', '打开后焦点落在首个菜单项');
  assert.ok(opened.left >= 8 && opened.right <= opened.viewport - 7, `菜单不得溢出视口：${JSON.stringify(opened)}`);
  const keyboard = await evaluate(`(()=>{document.querySelector('#card-menu [data-card-action="play"]').focus();document.activeElement.dispatchEvent(new KeyboardEvent('keydown',{key:'ArrowDown',bubbles:true}));const next=document.activeElement.dataset.cardAction;document.activeElement.dispatchEvent(new KeyboardEvent('keydown',{key:'Escape',bubbles:true}));return {next,closed:!document.querySelector('#card-menu'),expanded:document.querySelector('.wall-card [data-more]').getAttribute('aria-expanded'),restored:document.activeElement?.dataset.more!==undefined};})()`);
  assert.equal(keyboard.next, 'probe', '方向键在菜单项间移动');
  assert.equal(keyboard.closed, true); assert.equal(keyboard.expanded, 'false'); assert.equal(keyboard.restored, true, 'Esc 关闭后焦点回到触发按钮');
  const context = await evaluate(`(()=>{const card=document.querySelector('.wall-card'),r=card.getBoundingClientRect();card.dispatchEvent(new MouseEvent('contextmenu',{bubbles:true,cancelable:true,clientX:r.left+40,clientY:r.top+40}));const menu=document.querySelector('#card-menu'),rect=menu.getBoundingClientRect();return {open:!!menu,left:Math.round(rect.left),top:Math.round(rect.top),x:Math.round(r.left+40),y:Math.round(r.top+40)};})()`);
  assert.equal(context.open, true, '右键唤出同一菜单');
  assert.ok(Math.abs(context.left - context.x) <= 1 && Math.abs(context.top - context.y) <= 1, `右键菜单贴着指针：${JSON.stringify(context)}`);
  const detail = await evaluate(`(async()=>{document.querySelector('#card-menu [data-card-action="detail"]').click();for(let i=0;i<100&&!document.querySelector('#detail-overlay')?.open;i++)await new Promise(r=>setTimeout(r,20));const state={open:document.querySelector('#detail-overlay').open,menuGone:!document.querySelector('#card-menu')};closeDetail();return state;})()`);
  assert.equal(detail.open, true); assert.equal(detail.menuGone, true);
  const outside = await evaluate(`(()=>{const b=document.querySelector('.wall-card [data-more]');b.click();document.body.dispatchEvent(new PointerEvent('pointerdown',{bubbles:true}));return {closed:!document.querySelector('#card-menu'),expanded:b.getAttribute('aria-expanded')};})()`);
  assert.equal(outside.closed, true); assert.equal(outside.expanded, 'false');
  assert.deepEqual(await evaluate('__errors'), []);
  assert.equal(await evaluate(`__calls.filter(c=>c.method!=='GET').length`), 0, '打开/关闭菜单不产生写请求');
});

test('概览最近活动：显示播放记录摘要、UTC 时间、全量记录入口且只读', { timeout: 60000 }, async t => {
  if (!available(t)) return;
  const { evaluate } = await browser(t);
  await evaluate(`(()=>{window.__playbackEvents=[
    {user_id:1,user_name:'owner',movie_id:7,movie_title:'测试影片 A',play_count:3,last_at:'2026-11-15T09:12:33Z',position_ticks:600000000,last_kind:'progress'},
    {user_id:2,user_name:'',movie_id:9,movie_title:'',play_count:1,last_at:'2026-11-14T20:00:00Z',position_ticks:0,last_kind:'start'}
  ]})()`);
  await evaluate(`(async()=>{await page('overview');})()`);
  await new Promise(r => setTimeout(r, 500));
  const shown = await evaluate(`(()=>{
    const box = document.querySelector('#overview-activity');
    const rows = [...box.querySelectorAll('.dash-activity')];
    return {rows: rows.length,
      titles: rows.map(r => r.querySelector('strong').textContent),
      details: rows.map(r => r.querySelector('small').textContent),
      link: document.querySelector('#overview-activity-title').closest('.dash-panel-head').querySelector('a').getAttribute('href'),
      posts: __calls.filter(c=>c.method==='POST').length};
  })()`);
  assert.equal(shown.rows, 2);
  assert.deepEqual(shown.titles, ['测试影片 A', '#9']);
  assert.match(shown.details[0], /^owner · 3 次 · 2026-11-15 09:12 UTC · /);
  assert.match(shown.details[1], /^已删除用户 · 1 次 · 2026-11-14 20:00 UTC · /);
  assert.equal(shown.link, '#firewall');
  assert.equal(shown.posts, 0, '首页保持只读');
  await evaluate(`(()=>{window.__playbackEvents=[]})()`);
  await evaluate(`(async()=>{await page('overview');})()`);
  await new Promise(r => setTimeout(r, 400));
  assert.match(await evaluate(`document.querySelector('#overview-activity').textContent`), /最近还没有播放记录/);
  assert.deepEqual(await evaluate('__errors'), []);
});

test('媒体墙布局引擎：五种布局、卡片变体把标题压到图上、切换不重发请求并记住偏好', { timeout: 60000 }, async t => {
  if (!available(t)) return;
  const { evaluate } = await browser(t);
  await evaluate(`(async()=>{await page('items');})()`);
  const wallRequests = `__calls.filter(c=>c.url.includes('/emby/Users/1/Items')).length`;
  const before = await evaluate(`(()=>({
    value:document.querySelector('#item-layout').value,
    options:[...document.querySelectorAll('#item-layout option')].map(o=>o.value),
    layout:document.querySelector('#wall').className,
    stored:localStorage.getItem('emby_wall_layout'),
    requests:${wallRequests},
    metas:document.querySelectorAll('#wall .wall-card .wall-meta').length}))()`);
  assert.equal(before.value, 'poster');
  assert.deepEqual(before.options, ['poster', 'posterCard', 'thumb', 'thumbCard', 'list']);
  assert.match(before.layout, /wall-layout-poster/);
  assert.equal(before.stored, null);
  assert.ok(before.metas > 0, '默认海报布局的标题在图片下方');
  await evaluate(`(()=>{const select=document.querySelector('#item-layout');select.value='thumbCard';select.dispatchEvent(new Event('change',{bubbles:true}));})()`);
  const after = await evaluate(`(()=>({
    layout:document.querySelector('#wall').className,
    stored:localStorage.getItem('emby_wall_layout'),
    focused:document.activeElement?.id,
    url:location.search,
    requests:${wallRequests},
    thumbs:document.querySelectorAll('#wall .wall-poster.is-thumb').length,
    captions:document.querySelectorAll('#wall .wall-card.has-caption .wall-caption').length,
    metas:document.querySelectorAll('#wall .wall-card .wall-meta').length,
    captionTitle:document.querySelector('#wall .wall-caption .wall-title')?.textContent}))()`);
  assert.match(after.layout, /wall-layout-thumbCard/);
  assert.equal(after.stored, 'thumbCard');
  assert.equal(after.focused, 'item-layout');
  assert.equal(after.url, '', '布局偏好不进 URL');
  assert.equal(after.requests, before.requests, '切布局不应重发列表请求');
  assert.ok(after.thumbs > 0 && after.captions > 0, '缩略图卡片应换成 16:9 并带标题层');
  assert.equal(after.metas, 0, '卡片变体的标题只出现一次');
  assert.ok(after.captionTitle && after.captionTitle.length > 0);
  await evaluate(`(()=>{const select=document.querySelector('#item-layout');select.value='list';select.dispatchEvent(new Event('change',{bubbles:true}));})()`);
  const listed = await evaluate(`(()=>({layout:document.querySelector('#wall').className, stored:localStorage.getItem('emby_wall_layout')}))()`);
  assert.match(listed.layout, /wall-list/);
  assert.equal(listed.stored, 'list');
  // 老版本存的是 grid；升级后回落到 poster，不把老用户丢进不存在的布局。
  await evaluate(`(()=>{localStorage.setItem('emby_wall_layout','grid');})()`);
  await evaluate(`(async()=>{await page('items');})()`);
  const legacy = await evaluate(`(()=>({value:document.querySelector('#item-layout').value, layout:document.querySelector('#wall').className}))()`);
  assert.equal(legacy.value, 'poster');
  assert.match(legacy.layout, /wall-layout-poster/);
  assert.deepEqual(await evaluate('__errors'), []);
});

test('媒体墙筛选多选：收藏与播放状态同时生效、URL 与气泡回显、旧链接自动迁移', { timeout: 60000 }, async t => {
  if (!available(t)) return;
  const { evaluate } = await browser(t);
  const wallUrls = `__calls.filter(c=>c.url.includes('/emby/Users/1/Items')).map(c=>decodeURIComponent(c.url))`;
  await evaluate(`(async()=>{history.replaceState(null,'',location.pathname+'?status=played&favorite=1#items');await page('items');for(let i=0;i<100&&!document.querySelector('#wall-active-filters');i++)await new Promise(r=>setTimeout(r,20));})()`);
  const shown = await evaluate(`(()=>({
    played:document.querySelector('[data-status="played"]').getAttribute('aria-pressed'),
    favorite:document.querySelector('[data-favorite]').getAttribute('aria-pressed'),
    chips:[...document.querySelectorAll('#wall-active-filters .filter-chip')].map(c=>c.textContent),
    url:location.search,
    filtered:${wallUrls}.filter(u=>u.includes('Filters='))}))()`);
  assert.equal(shown.played, 'true');
  assert.equal(shown.favorite, 'true');
  assert.ok(shown.chips.some(text => text.includes('状态：已看')), '状态气泡应显示当前值');
  assert.ok(shown.chips.some(text => text.includes('收藏：仅收藏')), '收藏应有独立气泡');
  assert.equal(shown.url, '?status=played&favorite=1');
  assert.ok(shown.filtered.some(url => /Filters=(IsFavorite,IsPlayed|IsPlayed,IsFavorite)/.test(url)), '两个维度应落成一组 AND 的 Filters');
  // 只关掉收藏，播放状态继续生效。
  await evaluate(`document.querySelector('[data-favorite]').click()`);
  await new Promise(r => setTimeout(r, 400));
  const cleared = await evaluate(`(()=>({
    favorite:document.querySelector('[data-favorite]').getAttribute('aria-pressed'),
    url:location.search,
    filtered:${wallUrls}}))()`);
  assert.equal(cleared.favorite, 'false');
  assert.ok(!cleared.url.includes('favorite'), 'URL 里不再保留 favorite');
  assert.match(cleared.filtered[cleared.filtered.length - 1], /Filters=IsPlayed/);
  assert.ok(!cleared.filtered[cleared.filtered.length - 1].includes('IsFavorite'));
  // 旧书签 ?status=favorite 迁移成 ?favorite=1，语义不丢。
  await evaluate(`(async()=>{history.replaceState(null,'',location.pathname+'?status=favorite#items');await page('items');for(let i=0;i<100&&!document.querySelector('[data-favorite]');i++)await new Promise(r=>setTimeout(r,20));})()`);
  const legacy = await evaluate(`(()=>({favorite:document.querySelector('[data-favorite]').getAttribute('aria-pressed'), url:location.search}))()`);
  assert.equal(legacy.favorite, 'true');
  assert.equal(legacy.url, '?favorite=1');
  assert.deepEqual(await evaluate('__errors'), []);
});

test('概览运行中任务：显示类型与已运行时长、查看跳任务中心且首页保持只读', { timeout: 60000 }, async t => {
  if (!available(t)) return;
  const { evaluate } = await browser(t);
  await evaluate(`(()=>{window.__taskItems=[
    {id:41,type:'scan',status:'running',started_at:new Date(Date.now()-65000).toISOString()},
    {id:42,type:'probe',status:'running',started_at:new Date(Date.now()-5000).toISOString()},
    {id:40,type:'scan',status:'success',started_at:'2026-11-15T00:00:00Z',ended_at:'2026-11-15T00:01:00Z'}
  ]})()`);
  await evaluate(`(async()=>{await page('overview');})()`);
  await new Promise(r => setTimeout(r, 1300));
  const shown = await evaluate(`(()=>{
    const box = document.querySelector('#overview-tasks'), count = document.querySelector('[data-dash="tasks-count"]');
    const rows = [...box.querySelectorAll('.dash-task')];
    return {count: count.textContent, countRows: rows.length, labels: rows.map(row => row.querySelector('strong').textContent),
      elapsed: rows.map(row => row.querySelector('small').textContent),
      stops: rows.map(row => !!row.querySelector('[data-task-cancel]')),
      opens: box.querySelectorAll('[data-task-open]').length};
  })()`);
  assert.equal(shown.count, '2 个运行中'); assert.equal(shown.countRows, 2);
  assert.deepEqual(shown.labels, ['扫描媒体库', '媒体信息探测']);
  assert.match(shown.elapsed[0], /^已运行 1 分 /); assert.match(shown.elapsed[1], /^已运行 \d+ 秒$/);
  assert.deepEqual(shown.stops, [false, false], '首页保持只读，停止入口留在计划任务页与任务中心');
  assert.equal(shown.opens, 2);
  assert.deepEqual(await evaluate("__calls.filter(c=>c.method==='POST').map(c=>c.url)"), []);
  await evaluate(`document.querySelector('#overview-tasks [data-task-open]').click()`);
  await evaluate(`new Promise(r=>{const check=()=>location.hash==='#tasks'?r():setTimeout(check,20);check();})`);
  assert.equal(await evaluate('location.hash'), '#tasks');
  assert.deepEqual(await evaluate('__errors'), []);
});

test('运行日志历史模式：读磁盘 app 日志、暂停实时尾随、可返回实时', { timeout: 60000 }, async t => {
  if (!available(t)) return;
  const { evaluate } = await browser(t);
  await evaluate(`(async()=>{await page('logs');})()`);
  await evaluate(`new Promise(r=>{const check=()=>document.querySelector('#dl-body').textContent.includes('auto-log-')?r():setTimeout(check,20);check();})`);
  const live = await evaluate(`document.querySelector('#dl-status').textContent`);
  assert.match(live, /已连接/);
  await evaluate(`document.querySelector('#dl-history').click()`);
  await evaluate(`new Promise(r=>{const check=()=>document.querySelector('#dl-body').textContent.includes('2026-10-29T12:00:00Z info auto-log-')?r():setTimeout(check,20);check();})`);
  const history = await evaluate(`(()=>({text:document.querySelector('#dl-body').textContent,status:document.querySelector('#dl-status').textContent,pressed:document.querySelector('#dl-history').getAttribute('aria-pressed'),note:document.querySelector('#dl-gap').textContent,earlier:document.querySelector('#dl-earlier').hidden}))()`);
  assert.match(history.text, /2026-10-29T12:00:00Z info auto-log-/);
  assert.match(history.status, /历史模式/);
  assert.equal(history.pressed, 'true');
  assert.match(history.note, /已加载 1 条/);
  assert.equal(history.earlier, true, '没有更早一页时不显示「加载更早」');
  await evaluate(`document.querySelector('#dl-history').click()`);
  await evaluate(`new Promise(r=>{const check=()=>document.querySelector('#dl-body').textContent.includes('auto-log-')?r():setTimeout(check,20);check();})`);
  const back = await evaluate(`(()=>({status:document.querySelector('#dl-status').textContent,pressed:document.querySelector('#dl-history').getAttribute('aria-pressed')}))()`);
  assert.match(back.status, /已连接/);
  assert.equal(back.pressed, 'false');
  assert.deepEqual(await evaluate('__errors'), []);
});

test('计划任务运行态：执行中角标、已运行时长与可停止类型', { timeout: 60000 }, async t => {
  if (!available(t)) return;
  const { evaluate } = await browser(t);
  await evaluate(`(()=>{window.__scheduledItems={items:[
    {id:1,name:'夜间扫描',type:'scan',cron:'0 3 * * *',params:'{}',enabled:true,config_etag:'f1',running:true,started_at:new Date(Date.now()-65000).toISOString()},
    {id:2,name:'补探测',type:'probe',cron:'0 4 * * *',params:'{}',enabled:true,config_etag:'f2',running:true,started_at:new Date(Date.now()-5000).toISOString()},
    {id:3,name:'日常刮削',type:'scrape',cron:'0 5 * * *',params:'{}',enabled:true,config_etag:'f3',last_status:'success',last_run_at:'2026-11-15T00:00:00Z',last_message:'完成'}
  ],types:{scan:'扫描媒体库',probe:'媒体信息探测',scrape:'内置刮削'}}})()`);
  await evaluate(`(async()=>{await page('scheduled');})()`);
  await new Promise(r => setTimeout(r, 1300));
  const shown = await evaluate(`(()=>{
    const state = id => document.querySelector('[data-scheduled-state="'+id+'"]').textContent.trim();
    const stop = id => document.querySelector('[data-role="stop"][data-id="'+id+'"]').hidden;
    const run = id => document.querySelector('[data-role="run"][data-id="'+id+'"]').disabled;
    const elapsed = id => { const el = document.querySelector('[data-scheduled-elapsed="'+id+'"]'); return el.hidden ? null : el.textContent; };
    return {scanBadge: state(1), probeBadge: state(2), idleBadge: state(3), scanStop: stop(1), probeStop: stop(2), idleStop: stop(3), scanRun: run(1), idleRun: run(3), scanElapsed: elapsed(1), probeElapsed: elapsed(2), idleElapsed: elapsed(3)};
  })()`);
  assert.equal(shown.scanBadge, '进行中'); assert.equal(shown.probeBadge, '进行中'); assert.equal(shown.idleBadge, '成功');
  assert.equal(shown.scanStop, true, '扫库不支持中途停止'); assert.equal(shown.probeStop, false); assert.equal(shown.idleStop, true);
  assert.equal(shown.scanRun, true); assert.equal(shown.idleRun, false);
  assert.match(shown.scanElapsed, /^已运行 1 分 /); assert.match(shown.probeElapsed, /^已运行 \d+ 秒$/); assert.equal(shown.idleElapsed, null);
  // 「停止」复用领域既有的取消端点，不新增一套任务取消 API。
  const stopped = await evaluate(`(async()=>{document.querySelector('[data-role="stop"][data-id="2"]').click();for(let i=0;i<100&&!__calls.some(c=>c.url.endsWith('/probe/media/cancel'));i++)await new Promise(r=>setTimeout(r,20));return __calls.filter(c=>c.method==='POST').map(c=>c.url);})()`);
  assert.deepEqual(stopped, ['/api/admin/probe/media/cancel']);
  await evaluate(`(()=>{window.__scheduledItems.items[0].running=false;window.__scheduledItems.items[0].last_status='success';window.__scheduledItems.items[1].running=false;})()`);
  await new Promise(r => setTimeout(r, 5600));
  const after = await evaluate(`(()=>({badge:document.querySelector('[data-scheduled-state="1"]').textContent.trim(),stop:document.querySelector('[data-role="stop"][data-id="1"]').hidden,run:document.querySelector('[data-role="run"][data-id="1"]').disabled,elapsed:document.querySelector('[data-scheduled-elapsed="1"]').hidden}))()`);
  assert.deepEqual(after, { badge: '成功', stop: true, run: false, elapsed: true }, '轮询同步结束态并解除禁用');
  assert.deepEqual(await evaluate('__errors'), []);
});

test('计划任务布局：表格与卡片互换、偏好记忆、运行态沿用同一套锚点', { timeout: 60000 }, async t => {
  if (!available(t)) return;
  const { evaluate } = await browser(t);
  await evaluate(`(()=>{localStorage.removeItem('emby_scheduled_layout');window.__scheduledItems={items:[
    {id:1,name:'夜间扫描',type:'scan',cron:'0 3 * * *',params:'{}',enabled:true,config_etag:'f1',running:true,started_at:new Date(Date.now()-5000).toISOString()},
    {id:2,name:'补探测',type:'probe',cron:'0 4 * * *',params:'{}',enabled:true,config_etag:'f2',running:true,started_at:new Date(Date.now()-5000).toISOString()}
  ],types:{scan:'扫描媒体库',probe:'媒体信息探测'}}})()`);
  await evaluate(`(async()=>{await page('scheduled');})()`);
  const initial = await evaluate(`(()=>({select:document.querySelector('#sched-layout').value,table:!!document.querySelector('.table-wrap.scheduled-table'),cards:document.querySelectorAll('.sched-card').length}))()`);
  assert.deepEqual(initial, { select: 'table', table: true, cards: 0 }, '默认沿用表格视图');
  await evaluate(`(async()=>{const el=document.querySelector('#sched-layout');el.value='card';el.dispatchEvent(new Event('change'));for(let i=0;i<100&&!document.querySelector('.sched-card');i++)await new Promise(r=>setTimeout(r,20));})()`);
  const cards = await evaluate(`(()=>({cards:document.querySelectorAll('.sched-card').length,table:!!document.querySelector('.table-wrap.scheduled-table'),stored:localStorage.getItem('emby_scheduled_layout'),focus:document.activeElement?.id,scanBadge:document.querySelector('[data-scheduled-state="1"]').textContent.trim(),scanStop:document.querySelector('[data-role="stop"][data-id="1"]').hidden,probeStop:document.querySelector('[data-role="stop"][data-id="2"]').hidden,url:location.search}))()`);
  assert.equal(cards.cards, 2);
  assert.equal(cards.table, false, '卡片视图不再渲染表格');
  assert.equal(cards.stored, 'card');
  assert.equal(cards.focus, 'sched-layout', '切换后焦点留在布局选择上');
  assert.equal(cards.scanBadge, '进行中', '运行态锚点在卡片里同样生效');
  assert.equal(cards.scanStop, true); assert.equal(cards.probeStop, false);
  assert.equal(cards.url, '', '布局偏好不进 URL');
  // 重新进入页面仍记住卡片视图
  await evaluate(`(async()=>{await page('overview');await page('scheduled');for(let i=0;i<100&&!document.querySelector('.sched-card');i++)await new Promise(r=>setTimeout(r,20));})()`);
  assert.equal(await evaluate(`document.querySelector('#sched-layout').value`), 'card');
  assert.equal(await evaluate(`document.querySelectorAll('.sched-card').length`), 2);
  // 切回表格
  await evaluate(`(async()=>{const el=document.querySelector('#sched-layout');el.value='table';el.dispatchEvent(new Event('change'));for(let i=0;i<100&&!document.querySelector('.table-wrap.scheduled-table');i++)await new Promise(r=>setTimeout(r,20));})()`);
  assert.equal(await evaluate(`document.querySelectorAll('.sched-card').length`), 0);
  assert.equal(await evaluate(`localStorage.getItem('emby_scheduled_layout')`), 'table');
  assert.equal(await evaluate(`__calls.filter(c=>c.method!=='GET').length`), 0, '切换布局不产生写请求');
  assert.deepEqual(await evaluate('__errors'), []);
});

test('媒体墙筛选气泡：显示当前筛选、逐个清除并回到对应控件', { timeout: 60000 }, async t => {
  if (!available(t)) return;
  const { evaluate } = await browser(t);
  await evaluate(`(async()=>{history.replaceState(null,'',location.pathname+'?status=played&search=abc#items');await page('items');for(let i=0;i<100&&!document.querySelector('#wall-active-filters');i++)await new Promise(r=>setTimeout(r,20));})()`);
  const shown = await evaluate(`(()=>({labels:[...document.querySelectorAll('#wall-active-filters [data-filter-clear]')].map(b=>b.textContent.trim()),pressed:document.querySelector('[data-status="played"]').getAttribute('aria-pressed')}))()`);
  assert.deepEqual(shown.labels, ['状态：已看', '搜索：abc']);
  assert.equal(shown.pressed, 'true');
  const cleared = await evaluate(`(async()=>{document.querySelector('[data-filter-clear="status"]').click();for(let i=0;i<100&&new URLSearchParams(location.search).get('status');i++)await new Promise(r=>setTimeout(r,20));for(let i=0;i<100&&document.querySelector('[data-filter-clear="status"]');i++)await new Promise(r=>setTimeout(r,20));return {search:location.search,focus:document.activeElement?.dataset?.status,chips:[...document.querySelectorAll('#wall-active-filters [data-filter-clear]')].map(b=>b.dataset.filterClear)};})()`);
  assert.doesNotMatch(cleared.search, /status=/); assert.equal(cleared.focus, '', '焦点回到「全部」状态');
  assert.deepEqual(cleared.chips, ['search']);
  const clearedSearch = await evaluate(`(async()=>{document.querySelector('[data-filter-clear="search"]').click();for(let i=0;i<100&&document.querySelector('#wall-active-filters');i++)await new Promise(r=>setTimeout(r,20));return {search:location.search,focus:document.activeElement?.id,row:!!document.querySelector('#wall-active-filters')};})()`);
  assert.equal(clearedSearch.row, false, '没有筛选时不显示气泡行');
  assert.doesNotMatch(clearedSearch.search, /search=/); assert.equal(clearedSearch.focus, 'item-search');
  assert.deepEqual(await evaluate('__errors'), []);
  assert.equal(await evaluate(`__calls.filter(c=>c.method!=='GET').length`), 0, '清除筛选不产生写请求');
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
        const hits = await evaluate(`(()=>{return [...document.querySelectorAll('.wall-card:first-child [data-more],.wall-card:first-child [data-translate-select]')].map(b=>{b.scrollIntoView({block:'center'});const target=b.matches('input')?b.closest('label'):b;target.scrollIntoView({block:'center'});const r=target.getBoundingClientRect(),hit=document.elementFromPoint(r.x+r.width/2,r.y+r.height/2);return {ok:hit===target||target.contains(hit),width:r.width,height:r.height,button:b.tagName==='BUTTON'};});})()`);
        assert.equal(hits.length,2);if(width<=860)assert.ok(hits.every(h=>h.ok),`card hit targets at ${width}: ${JSON.stringify(hits)}`);
        if(width<=860)assert.ok(hits.filter(h=>h.button).every(h=>h.width>=43.99&&h.height>=43.99),`card touch dimensions at ${width}`);
        const detail = await evaluate(`(async()=>{await openMaintenanceDetail({id:1});const maintenance=document.querySelector('.detail-maintenance');if(!maintenance)return {missing:true};const collapsed=!maintenance.open;maintenance.open=true;document.querySelector('#detail-edit-toggle').click();await new Promise(r=>setTimeout(r,20));const result={collapsed,scroll:document.documentElement.scrollWidth,shown:!document.querySelector('#detail-edit').hidden};document.querySelector('#detail-close').click();return result;})()`);
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


test('日志 SSE：原文安全、来源切换、断流游标恢复、暂停与页面清理', {timeout:30000}, async t => {
  if (!available(t)) return;
  const {evaluate}=await browser(t);
  await evaluate(`(async()=>{window.__logRawText='<img src=x onerror="window.__logXSS=true">\\n\\n{"level":"info"}\\n  continuation 中文🙂\\n';await page('logs');for(let i=0;i<50&&!document.querySelector('#dl-body')?.textContent;i++)await new Promise(r=>setTimeout(r,10));})()`);
  const initial=await evaluate(`({text:document.querySelector('#dl-body').textContent,raw:__logRawText,xss:!!document.querySelector('#dl-body img')||!!window.__logXSS,live:document.querySelector('#dl-body').getAttribute('aria-live'),token:__logStreams[0].token,kind:__logStreams[0].kind})`);
  assert.equal(initial.text,initial.raw);assert.equal(initial.xss,false);assert.equal(initial.live,null);assert.equal(initial.token,'layout-fixture');assert.equal(initial.kind,'app');
  const switched=await evaluate(`(async()=>{window.__logRawText='GET /Items?token=raw-secret\\n';const selector=document.querySelector('#dl-kind');selector.value='request';selector.dispatchEvent(new Event('change'));for(let i=0;i<50&&!document.querySelector('#dl-body').textContent;i++)await new Promise(r=>setTimeout(r,10));return {aborted:__logStreams[0].aborted,text:document.querySelector('#dl-body').textContent,kind:__logStreams.at(-1).kind,url:__logStreams.at(-1).url,source:document.querySelector('#dl-source').textContent};})()`);
  assert.equal(switched.aborted,true);assert.equal(switched.text,'GET /Items?token=raw-secret\n');assert.equal(switched.kind,'request');assert.doesNotMatch(switched.url,/after=/);assert.match(switched.source,/request-YYYY-MM-DD/);
  const disconnected=await evaluate(`(async()=>{const count=__logStreams.length;__logStreams.at(-1).close();for(let i=0;i<150&&__logStreams.length===count;i++)await new Promise(r=>setTimeout(r,10));return {count:__logStreams.length,url:__logStreams.at(-1).url};})()`);
  assert.equal(disconnected.count,3);assert.match(disconnected.url,/kind=request&after=/);
  const paused=await evaluate(`(async()=>{document.querySelector('#dl-pause').click();const count=__logStreams.length;await new Promise(r=>setTimeout(r,500));return {count,after:__logStreams.length,aborted:__logStreams.at(-1).aborted,pressed:document.querySelector('#dl-pause').getAttribute('aria-pressed')};})()`);
  assert.equal(paused.count,paused.after);assert.equal(paused.aborted,true);assert.equal(paused.pressed,'true');
  const resumed=await evaluate(`(async()=>{document.querySelector('#dl-pause').click();for(let i=0;i<50&&__logStreams.length<4;i++)await new Promise(r=>setTimeout(r,10));const count=__logStreams.length;await page('settings');await new Promise(r=>setTimeout(r,400));return {count,after:__logStreams.length,aborted:__logStreams.at(-1).aborted,writes:__calls.filter(c=>c.url.includes('/logs/stream')&&c.method!=='GET').length,errors:__errors};})()`);
  assert.equal(resumed.count,4);assert.equal(resumed.after,4);assert.equal(resumed.aborted,true);assert.equal(resumed.writes,0);assert.deepEqual(resumed.errors,[]);
});

test('合集浏览：封面卡片进成员列表、返回恢复、隐藏影片专属控件与真实键盘操作', {timeout:60000}, async t => {
  if (!available(t)) return;
  const {call,evaluate}=await browser(t);
  // 人工视觉核验用：EMBY_REPORT_ARTIFACT_DIR 指向 /tmp 下目录时落两张截图。
  const output=process.env.EMBY_REPORT_ARTIFACT_DIR;
  if(output){assert.ok(path.resolve(output).startsWith('/tmp/'));fs.mkdirSync(output,{recursive:true});}
  async function shot(name){if(!output)return;const result=await call('Page.captureScreenshot',{format:'png'});fs.writeFileSync(path.join(output,name+'.png'),Buffer.from(result.data,'base64'));}
  const listed=await evaluate(`(async()=>{
    window.__collectionItems=[{Id:'boxset:QlVH',Name:'系列合集 B',Type:'BoxSet',IsFolder:true,ChildCount:3,ImageTags:{Primary:'tag-b'}},{Id:'boxset:QUFB',Name:'另一合集 A',Type:'BoxSet',IsFolder:true,ChildCount:2}];
    await page('items');location.hash='#items';
    // file:// 夹具里只能用相对 URL，用 pathname 保留当前文档。
    history.replaceState({},'',location.pathname+'?view=collections#items');await pageItems();
    for(let i=0;i<60&&!document.querySelector('.collection-card');i++)await new Promise(r=>setTimeout(r,15));
    const cards=[...document.querySelectorAll('.collection-card')];
    return {count:cards.length,tags:cards.map(c=>c.tagName),labels:cards.map(c=>c.dataset.collection),
      marks:cards.map(c=>c.querySelector('.collection-mark')?.textContent),
      movieOnly:!!document.querySelector('.wall-maintenance'),
      quick:cards.some(c=>c.querySelector('[data-quickplay],[data-more]')),
      switchActive:document.querySelector('[data-wall-view="collections"]').getAttribute('aria-pressed'),
      url:location.search};
  })()`);
  assert.equal(listed.count,2);
  assert.deepEqual(listed.tags,['BUTTON','BUTTON']);
  assert.match(listed.labels[0],/^boxset:/);
  assert.ok(listed.marks.every(mark=>mark==='合集'));
  assert.equal(listed.movieOnly,false,'纯合集列表不显示影片批量维护');
  assert.equal(listed.quick,false,'合集卡片没有快捷播放/更多操作入口');
  assert.equal(listed.switchActive,'true');
  assert.match(listed.url,/view=collections/);
  await call('Emulation.setDeviceMetricsOverride',{width:1280,height:900,deviceScaleFactor:1,mobile:false});
  await evaluate("scrollTo(0,0)");await shot('collections-list-1280');

  const opened=await evaluate(`(async()=>{
    const card=document.querySelector('.collection-card');
    card.focus();card.click();
    for(let i=0;i<60&&!document.querySelector('#collection-title');i++)await new Promise(r=>setTimeout(r,15));
    const call=__calls.filter(c=>c.url.includes('/Items?')&&c.url.includes('ParentId')).at(-1);
    const q=new URL(call.url,location.origin).searchParams;
    const page=new URL(location.href,location.origin).searchParams;
    return {title:document.querySelector('#collection-title').textContent,
      back:!!document.querySelector('#collection-back'),hash:location.hash,
      collection:page.get('collection'),parentId:q.get('ParentId'),types:q.get('IncludeItemTypes'),
      search2:!!document.querySelector('#item-search'),movieOnly:!!document.querySelector('.wall-maintenance'),
      statusFilters:!!document.querySelector('.wall-status-filters'),
      cards:document.querySelectorAll('.wall-card').length};
  })()`);
  assert.equal(opened.title,'另一合集 A','首张卡片按标题序打开对应合集');
  assert.equal(opened.back,true);
  assert.equal(opened.hash,'#items');
  assert.match(opened.collection,/^boxset:/);
  assert.match(opened.parentId,/^boxset:/,'成员列表按合集 ParentId 查询');
  assert.equal(opened.types,'Movie','合集成员仍是影片');
  assert.equal(opened.search2,true,'成员列表保留搜索');
  assert.equal(opened.movieOnly,false,'合集成员列表不显示影片索引批量维护');
  assert.equal(opened.statusFilters,true,'成员仍可按收藏/已看筛选');
  assert.equal(opened.cards,2);
  await evaluate("scrollTo(0,0)");await shot('collection-members-1280');

  const backing=await evaluate(`(async()=>{
    document.querySelector('#collection-back').click();
    for(let i=0;i<60&&location.search.includes('collection');i++)await new Promise(r=>setTimeout(r,15));
    return {search:location.search,title:!!document.querySelector('#collection-title'),cards:document.querySelectorAll('.collection-card').length};
  })()`);
  assert.doesNotMatch(backing.search,/collection=/,'返回后退回合集列表');
  assert.equal(backing.title,false);
  assert.equal(backing.cards,2);

  // 版面与可操作性几何：窄屏不横向滚动、卡片触控目标、键盘焦点可见。
  const geometry=await evaluate(`(async()=>{
    const measure=async width=>{
      const card=document.querySelector('.collection-card');
      card.scrollIntoView({block:'center'});
      const box=card.getBoundingClientRect();
      card.focus();
      const style=getComputedStyle(card);
      return {width,scroll:document.documentElement.scrollWidth,cardW:Math.round(box.width),cardH:Math.round(box.height),
        outline:style.outlineStyle,outlineWidth:style.outlineWidth,focused:document.activeElement===card};
    };
    const wide=await measure(1280);return wide;
  })()`);
  assert.ok(geometry.scroll<=geometry.width+1,'合集列表在 1280 宽不横向滚动');
  assert.ok(geometry.cardW>=148,'卡片不小于媒体墙最小列宽');
  assert.ok(geometry.cardH>=44,'卡片满足 44px 触控高度');
  assert.equal(geometry.focused,true,'合集卡片可键盘聚焦');
  assert.notEqual(geometry.outline,'none','聚焦时有可见轮廓');
  await call('Emulation.setDeviceMetricsOverride',{width:375,height:900,deviceScaleFactor:1,mobile:true});
  const narrow=await evaluate(`(async()=>{const card=document.querySelector('.collection-card');card.scrollIntoView({block:'center'});const box=card.getBoundingClientRect();return {scroll:document.documentElement.scrollWidth,cardH:Math.round(box.height)};})()`);
  assert.ok(narrow.scroll<=376,'合集列表在 375 宽不横向滚动');
  assert.ok(narrow.cardH>=44,'窄屏卡片仍满足 44px 触控高度');
  await shot('collections-list-375');
  await call('Emulation.setDeviceMetricsOverride',{width:1280,height:900,deviceScaleFactor:1,mobile:false});

  const keyboard=await evaluate(`(async()=>{
    const btn=[...document.querySelectorAll('[data-wall-view]')].find(b=>b.dataset.wallView==='movies');
    btn.focus();btn.click();
    for(let i=0;i<60&&location.search.includes('view=collections');i++)await new Promise(r=>setTimeout(r,15));
    return {search:location.search,focused:document.activeElement.dataset.wallView||'',errors:__errors};
  })()`);
  assert.doesNotMatch(keyboard.search,/view=collections/,'切回影片视图');
  assert.equal(keyboard.focused,'movies','切换后把焦点还给当前视图按钮');
  assert.deepEqual(keyboard.errors,[]);
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

test('详情抽屉：窄屏下元数据、演员与媒体流表格都不溢出', { timeout: 60000 }, async t => {
  if (!available(t)) return;
  const { call, evaluate } = await browser(t);
  await evaluate(`(()=>{
    window.__detailActors=[{name:'演员名字很长很长的一位',has_image:false},{name:'二号演员',has_image:false},{name:'三号演员',has_image:false}];
    window.__detailFiles=[{role:'main',index:0,name:'very-long-file-name-'+'x'.repeat(60)+'.mkv',size:1234567,probed:true,path:'/media/library/['+'超长目录名'.repeat(6)+']/very-long-'+'y'.repeat(80)+'.mkv',streams:[
      {Index:0,Type:'Video',Codec:'hevc',Width:3840,Height:2160,BitRate:12345678,Profile:'Main 10',BitDepth:10},
      {Index:1,Type:'Audio',Codec:'eac3',Channels:6,ChannelLayout:'5.1(side)',BitRate:640000,Language:'jpn',SampleRate:48000},
      {Index:2,Type:'Subtitle',Codec:'pgs',Language:'chi',IsDefault:true,IsExternal:true}]}];
  })()`);
  for (const width of [390, 320]) {
    await evaluate(`(async()=>{closeDetail();await openMaintenanceDetail({id:1});for(let i=0;i<100&&!document.querySelector('.avatar-card');i++)await new Promise(r=>setTimeout(r,20));})()`);
    await call('Emulation.setDeviceMetricsOverride', { width, height: 844, deviceScaleFactor: 1, mobile: true });
    const m = await evaluate(`(()=>{
      const drawer=document.querySelector('#detail-drawer');
      const dr=drawer.getBoundingClientRect();
      const rows=[...drawer.querySelectorAll('.detail-meta-row')].map(row=>{const dd=row.querySelector('dd').getBoundingClientRect();return {label:row.querySelector('dt').getBoundingClientRect().width,ddW:dd.width,ddOverflow:row.querySelector('dd').scrollWidth-row.querySelector('dd').clientWidth};});
      const cards=[...drawer.querySelectorAll('.avatar-card')].map(c=>{const r=c.getBoundingClientRect();return {w:r.width,right:r.right};});
      const grids=[...drawer.querySelectorAll('.avatar-grid')].map(g=>g.getBoundingClientRect().width);
      const wraps=[...drawer.querySelectorAll('.detail-section .table-wrap')].map(w=>{const table=w.querySelector('table');const rows=[...w.querySelectorAll('tbody tr')];return {cw:w.clientWidth,sw:w.scrollWidth,right:w.getBoundingClientRect().right,overflowX:getComputedStyle(w).overflowX,minWidth:getComputedStyle(table).minWidth,theadDisplay:getComputedStyle(w.querySelector('thead')).display,rowCount:rows.length,rowDisplay:rows.map(r=>getComputedStyle(r).display),tdLabels:rows[0]?[...rows[0].children].map(td=>({label:getComputedStyle(td,'::before').content,display:getComputedStyle(td).display,whiteSpace:getComputedStyle(td).whiteSpace})):[]};});
      const head=drawer.querySelector('.detail-file-head');
      const path=drawer.querySelector('.detail-path');
      const headKids=[...head.children].map(k=>({right:k.getBoundingClientRect().right,sw:k.scrollWidth-cw(k)}));
      function cw(el){return el.clientWidth;}
      return {drawerW:dr.width,drawerSW:drawer.scrollWidth,drawerCW:drawer.clientWidth,rows,cards,grids,wraps,headKids,pathOverflow:path.scrollWidth-path.clientWidth,pageScroll:document.documentElement.scrollWidth,width:innerWidth};
    })()`);
    assert.equal(m.drawerW, width, `${width}px: 抽屉占满视口宽度`);
    assert.ok(m.drawerSW <= m.drawerCW + 1, `${width}px: 抽屉内部不横向溢出（sw ${m.drawerSW} / cw ${m.drawerCW}）`);
    for (const row of m.rows) {
      assert.equal(row.ddOverflow, 0, `${width}px: 元数据值不撑破行`);
      assert.ok(row.ddW >= 120, `${width}px: 元数据值仍有可读宽度，实际 ${row.ddW}`);
    }
    for (const card of m.cards) {
      assert.ok(card.w >= 76, `${width}px: 演员卡不小于最小列宽，实际 ${card.w}`);
      assert.ok(card.right <= m.drawerW, `${width}px: 演员卡不越过抽屉`);
    }
    for (const grid of m.grids) assert.ok(grid <= m.drawerCW, `${width}px: 演员网格不越过抽屉内容区`);
    assert.ok(m.wraps.length >= 1, `${width}px: 媒体流区域存在`);
    for (const wrap of m.wraps) {
      // 窄屏下媒体流改成「每条一段」的卡片，不再横滚。
      assert.equal(wrap.overflowX, 'visible', `${width}px: 媒体流不再依赖横向滚动`);
      assert.equal(wrap.minWidth, '0px', `${width}px: 不再强制 560px 最小宽度`);
      assert.equal(wrap.theadDisplay, 'none', `${width}px: 窄屏隐藏表头，改用字段名标签`);
      assert.ok(wrap.cw <= m.drawerCW, `${width}px: 媒体流容器不越过抽屉内容区`);
      assert.ok(wrap.right <= m.drawerW, `${width}px: 媒体流不越过抽屉`);
      assert.ok(wrap.sw <= wrap.cw + 1, `${width}px: 卡片模式不横向溢出（sw ${wrap.sw} / cw ${wrap.cw}）`);
      assert.equal(wrap.rowCount, 3, `${width}px: 三条流各成一段`);
      for (const display of wrap.rowDisplay) assert.equal(display, 'block', `${width}px: 每条流是块级卡片`);
      const labels = wrap.tdLabels.map(td => td.label);
      for (const want of ['类型', '参数', '标记']) assert.ok(labels.some(label => label.includes(want)), `${width}px: 卡片显示「${want}」字段名，实际 ${labels.join('|')}`);
      const seq = wrap.tdLabels.find(td => td.label.includes('序号'));
      assert.ok(seq && seq.display === 'none', `${width}px: 序号行在卡片里隐藏`);
      for (const td of wrap.tdLabels) assert.equal(td.whiteSpace, 'normal', `${width}px: 卡片内允许换行`);
    }
    for (const kid of m.headKids) assert.ok(kid.right <= width, `${width}px: 文件头各段折行后仍在视口内`);
    assert.equal(m.pathOverflow, 0, `${width}px: 长路径不横向溢出`);
    assert.ok(m.pageScroll <= width + 1, `${width}px: 页面无横向滚动`);
    await evaluate(`closeDetail()`);
  }
  // 宽屏保持原来的表格 + 内部横滚，不因为窄屏改动而退化。
  await call('Emulation.setDeviceMetricsOverride', { width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
  await evaluate(`(async()=>{await openMaintenanceDetail({id:1});for(let i=0;i<100&&!document.querySelector('.stream-table');i++)await new Promise(r=>setTimeout(r,20));})()`);
  const wide = await evaluate(`(()=>{const w=document.querySelector('.stream-table');const table=w.querySelector('table');return {overflowX:getComputedStyle(w).overflowX,minWidth:getComputedStyle(table).minWidth,thead:getComputedStyle(w.querySelector('thead')).display,cw:w.clientWidth,sw:w.scrollWidth,tableW:table.getBoundingClientRect().width,rows:[...w.querySelectorAll('tbody tr')].map(r=>getComputedStyle(r).display)};})()`);
  assert.equal(wide.overflowX, 'auto', '宽屏媒体流仍在容器内横滚');
  // 560px 的强制最小宽度只在窄屏下用来托住列宽；宽屏由单元格内容自然撑开。
  assert.equal(wide.minWidth, '0px', '宽屏不套用窄屏的强制最小宽度');
  assert.notEqual(wide.thead, 'none', '宽屏显示表头');
  assert.ok(wide.tableW >= 400, `宽屏表格按内容展开，实际 ${wide.tableW}`);
  for (const display of wide.rows) assert.equal(display, 'table-row', '宽屏每条流仍是表格行');
  await evaluate(`closeDetail()`);
});

test('详情剧照：抽屉缩略图、大图浮层键盘翻页、Esc 归还焦点', { timeout: 60000 }, async t => {
  if (!available(t)) return;
  const { call, evaluate } = await browser(t);
  await evaluate(`(()=>{window.__detailBackdrops=[{index:0,tag:'t0'},{index:1,tag:'t1'},{index:2,tag:'t2'}];})()`);
  await evaluate(`(async()=>{await openMaintenanceDetail({id:1});for(let i=0;i<100&&!document.querySelector('.still-thumb');i++)await new Promise(r=>setTimeout(r,20));})()`);
  const strip = await evaluate(`(()=>{const thumbs=[...document.querySelectorAll('.still-thumb')];return {count:thumbs.length,srcs:thumbs.map(b=>b.querySelector('img').getAttribute('src')),heading:document.querySelector('.detail-stills')?.closest('.detail-section')?.querySelector('h3')?.textContent.trim()};})()`);
  assert.equal(strip.count, 3, '详情显示全部剧照缩略图');
  assert.deepEqual(strip.srcs, [
    '/emby/Items/1/Images/Backdrop/0?maxWidth=320&tag=t0',
    '/emby/Items/1/Images/Backdrop/1?maxWidth=320&tag=t1',
    '/emby/Items/1/Images/Backdrop/2?maxWidth=320&tag=t2'
  ]);
  assert.equal(strip.heading, '剧照 3 张');
  // 点第 2 张：浮层打开、大小图地址与计数正确、上一张可用下一张可用
  const opened = await evaluate(`(async()=>{const thumb=document.querySelectorAll('.still-thumb')[1];thumb.focus();thumb.click();for(let i=0;i<100&&!document.querySelector('#stills-overlay').open;i++)await new Promise(r=>setTimeout(r,20));return {open:document.querySelector('#stills-overlay').open,src:document.querySelector('#stills-image').getAttribute('src'),counter:document.querySelector('#stills-counter').textContent,prev:document.querySelector('#stills-prev').disabled,next:document.querySelector('#stills-next').disabled};})()`);
  assert.equal(opened.open, true);
  assert.match(opened.src, /\/emby\/Items\/1\/Images\/Backdrop\/1\?maxWidth=1600&tag=t1$/);
  assert.equal(opened.counter, '2 / 3');
  assert.equal(opened.prev, false); assert.equal(opened.next, false);
  // → 翻到末张，下一张禁用
  const next = await evaluate(`(async()=>{document.querySelector('#stills-overlay').dispatchEvent(new KeyboardEvent('keydown',{key:'ArrowRight',bubbles:true}));return {counter:document.querySelector('#stills-counter').textContent,src:document.querySelector('#stills-image').getAttribute('src'),next:document.querySelector('#stills-next').disabled};})()`);
  assert.equal(next.counter, '3 / 3'); assert.match(next.src, /Backdrop\/2\?maxWidth=1600/); assert.equal(next.next, true);
  // ← 回到第 2 张，Esc 关闭并归还焦点给触发的缩略图
  const closed = await evaluate(`(async()=>{document.querySelector('#stills-overlay').dispatchEvent(new KeyboardEvent('keydown',{key:'ArrowLeft',bubbles:true}));document.querySelector('#stills-overlay').dispatchEvent(new KeyboardEvent('keydown',{key:'Escape',bubbles:true}));document.querySelector('#stills-close').click();for(let i=0;i<100&&document.querySelector('#stills-overlay').open;i++)await new Promise(r=>setTimeout(r,20));return {open:document.querySelector('#stills-overlay').open,focus:document.activeElement?.dataset?.stillIndex,detail:document.querySelector('#detail-overlay').open};})()`);
  assert.equal(closed.open, false, '浮层关闭');
  assert.equal(closed.focus, '1', '焦点回到触发的缩略图');
  assert.equal(closed.detail, true, '只关剧照，详情抽屉还在');
  assert.equal(await evaluate(`__calls.filter(c=>c.method!=='GET').length`), 0, '看剧照不产生写请求');
  // 移动端：抽屉全宽、剧照条可横滑、浮层不溢出视口
  for (const width of [390, 320]) {
    await call('Emulation.setDeviceMetricsOverride', { width, height: 844, deviceScaleFactor: 1, mobile: true });
    await evaluate(`(async()=>{await openMaintenanceDetail({id:1});for(let i=0;i<100&&!document.querySelector('.still-thumb');i++)await new Promise(r=>setTimeout(r,20));document.querySelectorAll('.still-thumb')[0].click();for(let i=0;i<100&&!document.querySelector('#stills-overlay').open;i++)await new Promise(r=>setTimeout(r,20));
      // fixture 里图片请求不可达，换成真实尺寸的内联图再量布局，否则高度恒为 0。
      const img=document.querySelector('#stills-image');img.src='data:image/svg+xml;charset=utf-8,'+encodeURIComponent('<svg xmlns="http://www.w3.org/2000/svg" width="1600" height="900"></svg>');await new Promise(r=>{if(img.complete)return r();img.addEventListener('load',r,{once:true});img.addEventListener('error',r,{once:true});});})()`);
    const layout = await evaluate(`(()=>{const thumb=document.querySelector('.still-thumb').getBoundingClientRect();const strip=document.querySelector('.detail-stills');const img=document.querySelector('#stills-image').getBoundingClientRect();const shell=document.querySelector('.stills-shell').getBoundingClientRect();const prev=document.querySelector('#stills-prev').getBoundingClientRect();const next=document.querySelector('#stills-next').getBoundingClientRect();const drawer=document.querySelector('#detail-drawer').getBoundingClientRect();return {thumbW:thumb.width,imgW:img.width,imgH:img.height,shellW:shell.width,shellLeft:shell.left,vw:innerWidth,vh:innerHeight,prevW:prev.width,prevLeft:prev.left,nextRight:next.right,drawerW:drawer.width,stripScroll:strip.scrollWidth>strip.clientWidth,pageScroll:document.documentElement.scrollWidth};})()`);
    assert.ok(layout.thumbW <= width, `${width}px: 缩略图不越过视口`);
    assert.ok(Math.abs(layout.drawerW - width) <= 1, `${width}px: 抽屉在窄屏占满宽度，实际 ${layout.drawerW}`);
    assert.equal(layout.stripScroll, true, `${width}px: 剧照条自己横向滚动，不撑破抽屉`);
    assert.ok(layout.shellLeft >= 0 && layout.shellW <= width + 1, `${width}px: 浮层外壳不溢出`);
    assert.ok(layout.imgW >= width - 80, `${width}px: 大图吃满可用宽度，实际 ${layout.imgW}`);
    assert.ok(layout.imgH > 100 && layout.imgH <= layout.vh, `${width}px: 大图高度合理，实际 ${layout.imgH}`);
    assert.ok(layout.prevW >= 44, '触控目标不小于 44px');
    assert.ok(layout.prevLeft >= 0 && layout.nextRight <= width, `${width}px: 左右键留在视口内`);
    assert.ok(layout.pageScroll <= width + 1, `${width}px: 页面不出现横向滚动`);
    await evaluate(`closeStills();closeDetail();`);
  }
  await call('Emulation.setDeviceMetricsOverride', { width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
  assert.deepEqual(await evaluate('__errors'), []);
  await evaluate(`closeDetail()`);
  await evaluate(`(()=>{window.__detailBackdrops=[];})()`);
});

test('详情固定外壳：加载关闭、错误重试、长详情关闭、背景隔离、持久反馈与焦点归还' , {timeout:60000}, async t => {
  if (!available(t)) return;
  const {call,evaluate}=await browser(t);
  await call('Emulation.setDeviceMetricsOverride',{width:320,height:740,deviceScaleFactor:1,mobile:true});
  await evaluate("page('items')");
  // 详情抽屉是原生 <dialog>：模态性由浏览器保证，背景既不可聚焦也不随滚轮滚动。
  const loading=await evaluate(`(async()=>{const trigger=document.querySelector('.wall-title');trigger.focus();window.__detailDelay=true;void openDetail({Id:'1'});const overlay=document.querySelector('#detail-overlay');const result={close:!!document.querySelector('#detail-close'),focus:document.activeElement.id,open:overlay.open,modal:overlay.matches(':modal')};trigger.focus();result.escaped=!overlay.contains(document.activeElement);closeDetail();result.restored=document.activeElement===trigger;result.closed=!overlay.open;window.__detailDelay=false;window.__resolveDetail();await new Promise(r=>setTimeout(r,30));result.stale=!document.querySelector('#detail-content');return result;})()`);
  assert.equal(loading.close,true); assert.equal(loading.focus,'detail-close'); assert.equal(loading.open,true); assert.equal(loading.modal,true,'浮层以 showModal 打开'); assert.equal(loading.escaped,false,'模态期间背景不可聚焦'); assert.equal(loading.restored,true); assert.equal(loading.closed,true); assert.equal(loading.stale,true);
  const failureClose=await evaluate(`(async()=>{document.querySelector('.wall-title').focus();window.__detailFail=true;await openDetail({Id:'1'});const r=document.querySelector('#detail-close').getBoundingClientRect();return {x:r.x+r.width/2,y:r.y+r.height/2};})()`);
  await call('Input.dispatchTouchEvent',{type:'touchStart',touchPoints:[failureClose]});
  await call('Input.dispatchTouchEvent',{type:'touchEnd',touchPoints:[]});
  const exited=await evaluate(`({open:document.querySelector('#detail-overlay').open,focus:document.activeElement.classList.contains('wall-title')})`);
  assert.equal(exited.open,false); assert.equal(exited.focus,true);
  const failed=await evaluate(`(async()=>{window.__detailFail=true;await openDetail({Id:'1'});const result={retry:!!document.querySelector('#detail-retry'),close:!!document.querySelector('#detail-close'),focus:document.activeElement.id};window.__detailFail=false;document.querySelector('#detail-retry').click();await new Promise(r=>setTimeout(r,40));result.loaded=!!document.querySelector('.detail-plot');const drawer=document.querySelector('#detail-drawer');drawer.scrollTop=drawer.scrollHeight;const rect=document.querySelector('#detail-close').getBoundingClientRect();result.closeVisible=rect.top>=0&&rect.bottom<innerHeight;toast('持久操作错误','error');result.feedback=document.querySelector('#detail-overlay .overlay-feedback').textContent;await new Promise(r=>setTimeout(r,3500));result.persistent=document.querySelector('#detail-overlay .overlay-feedback').textContent;closeDetail();result.closed=!document.querySelector('#detail-overlay').open;return result;})()`);
  assert.equal(failed.retry,true);assert.equal(failed.close,true);assert.equal(failed.focus,'detail-close');assert.equal(failed.loaded,true);assert.equal(failed.closeVisible,true);assert.equal(failed.feedback,'持久操作错误');assert.equal(failed.persistent,'持久操作错误');assert.equal(failed.closed,true);
  const longFeedback=await evaluate(`(async()=>{await openDetail({Id:'1'});toast('长错误反馈'.repeat(500),'error');const feedback=document.querySelector('#detail-overlay .overlay-feedback');const height=feedback.getBoundingClientRect().height;feedback.scrollTop=feedback.scrollHeight;const close=document.querySelector('#detail-close').getBoundingClientRect();const result={height,limit:innerHeight*.25,readable:feedback.scrollHeight>feedback.clientHeight,close:close.top>=0&&close.bottom<innerHeight};closeDetail();return result;})()`);
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
      if (selector==='#detail-edit-save') await evaluate(`(async()=>{await openMaintenanceDetail({id:1});document.querySelector('.detail-maintenance').open=true;document.querySelector('#detail-edit-toggle').click();})()`);
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
