// Focused real-DOM source regressions. Not the exact-binary route matrix.
// Run explicitly: node web/test/onboarding-browser.mjs
// Or only long-dialog scroll visibility: node web/test/onboarding-browser.mjs --long-scroll-only
// Own ephemeral loopback static server only; no hub, credentials or providers.
import assert from "node:assert/strict";
import { once } from "node:events";
import { createServer } from "node:http";
import { createHash } from "node:crypto";
import { readFileSync, mkdirSync, writeFileSync, realpathSync, existsSync } from "node:fs";
import { resolve, extname, sep } from "node:path";
import { BidiSession } from "../../scripts/verify-phase1-auth.mjs";

const root = realpathSync(new URL("../../", import.meta.url));
const assets = resolve(root, "web/assets");
const longScrollOnly=process.argv.includes('--long-scroll-only');
const out = resolve(root, `.verification/round28/onboarding/${longScrollOnly?'scroll-boundary':'tab-boundary'}-source-dom`);
mkdirSync(out, { recursive: true, mode: 0o700 });
const main = readFileSync(resolve(assets, "js/main.js"), "utf8");
const callback = main.slice(main.indexOf("let addAgentRequest ="), main.indexOf("// Global update banner"));
const fixture = `
import { mountOverviewPage } from '/assets/js/pages/overview.js';
import { openAddAgentDialog } from '/assets/js/ui/shell.js';
import { createDialog } from '/assets/js/ui/dialog.js';
import { setCatalogs, setLang, t } from '/assets/js/core/i18n.js';
const catalogs = await Promise.all(['en','ko'].map(async lang => [lang, await (await fetch('/assets/i18n/'+lang+'.json')).json()]));
setCatalogs(Object.fromEntries(catalogs));
const commands = [
  {os:'linux',label:'Linux (systemd)',command:'SOURCE-TEST-NOT-EXECUTABLE'},
  {os:'darwin',label:'macOS (launchd)',command:'SOURCE-TEST-NOT-EXECUTABLE'},
  {os:'windows',label:'Windows (service)',command:'SOURCE-TEST-NOT-EXECUTABLE'}
];
let activePage = null;
const isAuthenticated = () => true;
const showToast = () => {};
const pending = [];
const getAgentToken = signal => new Promise((resolve,reject) => pending.push({resolve,reject,signal}));
${callback}
const nativeFetch = window.fetch.bind(window);
window.fetch = (input, options) => {
  const path = new URL(input,location.href).pathname;
  if (!path.startsWith('/api/')) return nativeFetch(input,options);
  const bodies = {'/api/v1/hosts':{hosts:[]},'/api/v1/egress':{hosts:[]},'/api/v1/buckets':{buckets:[],collectors:[]},'/api/v1/storage/accounts':{accounts:[]},'/api/v1/billing':{display_currency:null}};
  if (!(path in bodies)) throw new Error('unexpected source-test API '+path);
  return Promise.resolve(new Response(JSON.stringify(bodies[path]),{status:200,headers:{'Content-Type':'application/json'}}));
};
const mainEl = document.querySelector('main');
window.fixture = {
  pending, createDialog, keys:[],
  async mount(lang) {
    activePage?.teardown(); setLang(lang); document.documentElement.lang=lang;
    activePage = mountOverviewPage(mainEl,{onAddAgent:openAddAgent});
    await activePage.refresh();
  },
  finish(index) { pending[index].resolve({install_command:commands[0].command,install_commands:commands}); },
  replacePage() { activePage?.teardown(); activePage={teardown(){}}; mainEl.replaceChildren(); },
  helper(count=3,nonDismissable=false) {
    const modal=createDialog({titleId:'source-helper-title',title:'Source helper',nonDismissable});
    window.helperCount=0;
    const controls=Array.from({length:count},(_,i)=>{
      const node=document.createElement(i===0?'input':'button');node.id='helper-'+i;
      if(node.tagName==='BUTTON')node.type='button';node.textContent=i===count-1?'Close':'Count';
      if(i===1)node.addEventListener('click',()=>window.helperCount++);
      modal.body.append(node);return node;
    });
    const hidden=document.createElement('button');hidden.hidden=true;hidden.id='helper-hidden';
    const disabled=document.createElement('button');disabled.disabled=true;disabled.id='helper-disabled';
    const negative=document.createElement('button');negative.tabIndex=-1;negative.id='helper-negative';
    const fieldset=document.createElement('fieldset');fieldset.disabled=true;fieldset.innerHTML='<input id="helper-fieldset-disabled">';
    const inert=document.createElement('div');inert.inert=true;inert.innerHTML='<button id="helper-inert">Inert</button>';
    const invisible=document.createElement('input');invisible.style.visibility='hidden';invisible.id='helper-invisible';
    const unrendered=document.createElement('input');unrendered.style.display='none';unrendered.id='helper-unrendered';
    modal.body.append(hidden,disabled,negative,fieldset,inert,invisible,unrendered);
    window.helper={...modal,controls};modal.open();return true;
  },
};
document.addEventListener('keydown',event=>fixture.keys.push({key:event.key,trusted:event.isTrusted,prevented:event.defaultPrevented,target:event.target.id,active:document.activeElement.id,hasFocus:document.hasFocus()}));
Object.defineProperty(navigator,'clipboard',{configurable:true,value:{writeText:async()=>{throw new Error('Source-only clipboard denial')}}});
${longScrollOnly ? '' : "await window.fixture.mount('en');"}
window.ready=true;
`;
const html = `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><link rel="stylesheet" href="/assets/app.css"></head><body><main class="cp-main"></main><button id="elsewhere" type="button">Other action</button><script type="module" src="/fixture.mjs"></script></body></html>`;
const traffic = [];
const server = createServer((req, res) => {
  const path = new URL(req.url, "http://127.0.0.1").pathname;
  traffic.push({ path, method: req.method });
  if (req.method !== "GET") { res.writeHead(405).end(); return; }
  if (path === "/") { res.writeHead(200, { "Content-Type": "text/html" }).end(html); return; }
  if (path === "/fixture.mjs") { res.writeHead(200, { "Content-Type": "text/javascript" }).end(fixture); return; }
  if (!path.startsWith("/assets/")) { res.writeHead(404).end(); return; }
  try {
    const file = realpathSync(resolve(assets, decodeURIComponent(path.slice(8))));
    if (!file.startsWith(assets + sep)) { res.writeHead(403).end(); return; }
    const type = { ".js": "text/javascript", ".css": "text/css", ".json": "application/json" }[extname(file)] || "application/octet-stream";
    res.writeHead(200, { "Content-Type": type }).end(readFileSync(file));
  } catch { res.writeHead(404).end(); }
});
const session = new BidiSession();
const report = { kind: "A-source-real-DOM-only-not-exact-binary", checks: [], status: "running", screenshots: 0 };
report.dialogSHA256=createHash('sha256').update(readFileSync(resolve(assets,'js/ui/dialog.js'))).digest('hex');
report.cssSHA256=createHash('sha256').update(readFileSync(resolve(assets,'app.css'))).digest('hex');
const check = async (name, expression) => {
  const value = await session.evaluateJSON(expression);
  report.checks.push({ name, pass: value === true, value });
  if(value!==true)report.failedFocusState=await session.evaluateJSON(`({active:document.activeElement?.id,tag:document.activeElement?.tagName,hasFocus:document.hasFocus(),keys:fixture.keys.slice(-8),controls:[...document.querySelectorAll('dialog[open] input,dialog[open] button,dialog[open] [contenteditable]')].map(node=>({id:node.id,tabIndex:node.tabIndex,disabled:node.matches(':disabled'),hidden:node.hidden,rects:node.getClientRects().length}))})`);
  assert.equal(value, true, name);
};
const wait = async (expression) => {
  for (let i=0;i<100;i++) { if (await session.evaluate(expression)) return; await new Promise(done=>setTimeout(done,30)); }
  throw new Error("timed out: " + expression);
};
const shiftTab = async () => {
  await session.send('input.performActions',{context:session.context,actions:[{type:'key',id:'keyboard',actions:[{type:'keyDown',value:'\uE008'},{type:'keyDown',value:'\uE004'},{type:'keyUp',value:'\uE004'},{type:'keyUp',value:'\uE008'}]}]});
  await session.send('input.releaseActions',{context:session.context});
};
const focused = id => `document.hasFocus() && document.activeElement===document.getElementById('${id}')`;
async function longScrollCheck() {
  await session.evaluate(`(() => {
    document.getElementById('elsewhere').focus();
    const modal=fixture.createDialog({titleId:'long-dialog-title',title:'Long dialog source test'});
    const first=document.createElement('button');first.id='long-first';first.type='button';first.className='cp-btn cp-btn-secondary';first.textContent='First action';modal.body.append(first);
    for(let i=0;i<60;i++){const p=document.createElement('p');p.className='cp-muted-small';p.textContent='Long dialog instruction '+(i+1)+': source-only text to exercise the existing modal scrollport.';modal.body.append(p);}
    const last=document.createElement('button');last.id='long-last';last.type='button';last.className='cp-btn cp-btn-secondary';last.textContent='Last action';modal.body.append(last);
    window.scrollModal=modal;
    window.scrollFocus=()=>{
      const d=modal.dialog,r=d.getBoundingClientRect(),n=document.activeElement,a=n.getBoundingClientRect();
      const port={top:Math.max(0,r.top+d.clientTop),bottom:Math.min(innerHeight,r.top+d.clientTop+d.clientHeight),left:Math.max(0,r.left+d.clientLeft),right:Math.min(innerWidth,r.left+d.clientLeft+d.clientWidth)};
      return {id:n.id,hasFocus:document.hasFocus(),scrollTop:d.scrollTop,clientHeight:d.clientHeight,scrollHeight:d.scrollHeight,overflowY:getComputedStyle(d).overflowY,rect:{top:a.top,bottom:a.bottom,left:a.left,right:a.right},port,visible:a.bottom>port.top&&a.top<port.bottom&&a.right>port.left&&a.left<port.right};
    };
    modal.open();return true;
  })()`);
  await check('long helper uses actual overflowing native dialog CSS', "scrollModal.dialog instanceof HTMLDialogElement && scrollModal.dialog.scrollHeight>scrollModal.dialog.clientHeight && getComputedStyle(scrollModal.dialog).overflowY==='auto'");
  report.scrollStates=[{step:'opened',...await session.evaluateJSON('scrollFocus()')}];
  await session.pressTab();
  report.scrollStates.push({step:'interior native Tab to last',...await session.evaluateJSON('scrollFocus()')});
  await check('interior native Tab scrolls last control into the visible modal scrollport', "scrollFocus().id==='long-last' && scrollFocus().hasFocus && scrollFocus().visible && scrollFocus().scrollTop>0 && fixture.keys.at(-1).prevented===false");
  await session.pressTab();
  report.scrollStates.push({step:'last Tab wraps first',...await session.evaluateJSON('scrollFocus()')});
  await check('last Tab wraps first with its focus rect visible in modal scrollport', "scrollFocus().id==='long-first' && scrollFocus().hasFocus && scrollFocus().visible && fixture.keys.at(-1).trusted && fixture.keys.at(-1).prevented");
  await shiftTab();
  report.scrollStates.push({step:'first ShiftTab wraps last',...await session.evaluateJSON('scrollFocus()')});
  await check('first ShiftTab wraps last with its focus rect visible in modal scrollport', "scrollFocus().id==='long-last' && scrollFocus().hasFocus && scrollFocus().visible && fixture.keys.at(-1).trusted && fixture.keys.at(-1).prevented");
  await session.pressKey('\uE00C');await wait('!scrollModal.dialog.open');
  await check('long modal native Escape keeps existing opener restoration', focused('elsewhere'));
  await session.evaluate('scrollModal.dialog.remove();true');
}
try {
  server.listen(0, "127.0.0.1"); await once(server, "listening");
  assert.notEqual(server.address().port, 8090);
  await session.start(); await session.setViewport(360,800);
  const origin=`http://127.0.0.1:${server.address().port}`;
  report.blockedRequests=[];
  await session.send('network.addIntercept',{phases:['beforeRequestSent']});
  session.websocket.addEventListener('message',event=>{
    const message=JSON.parse(event.data);
    if(message.method!=='network.beforeRequestSent'||!message.params.isBlocked)return;
    const request=message.params.request,url=new URL(request.url);
    const allowed=url.origin===origin&&request.method==='GET';
    if(!allowed)report.blockedRequests.push({origin:url.origin,path:url.pathname,method:request.method});
    void session.send(allowed?'network.continueRequest':'network.failRequest',{request:request.request}).catch(error=>report.blockedRequests.push({error:String(error)}));
  });
  await session.send('session.subscribe',{events:['network.beforeRequestSent']});
  await session.navigate(`${origin}/#/`); await wait("window.ready===true");
  if(longScrollOnly) {
    await longScrollCheck();
  } else {
  for (const lang of ["en","ko"]) {
    await session.evaluate(`fixture.mount('${lang}')`);
    await check(`${lang}: all real overview heading targets resolve uniquely to H2`, `(() => {
      const sections=[...document.querySelectorAll('main section[aria-labelledby]')];
      return sections.length===4 && sections.every(section=>{
        const id=section.getAttribute('aria-labelledby'); const nodes=document.querySelectorAll('#'+id);
        return nodes.length===1 && nodes[0].tagName==='H2' && section.contains(nodes[0]) && !!nodes[0].textContent.trim();
      });
    })()`);
    await check(`${lang}: native CTA only after empty response, no automatic credential request`, "document.querySelector('#cp-onboarding-add-agent') instanceof HTMLButtonElement && document.querySelector('#cp-onboarding-add-agent').type==='button' && fixture.pending.length===0 && !document.querySelector('.cp-stat-card-value')");
  }
  await session.evaluate("document.getElementById('cp-onboarding-add-agent').focus();true"); await session.pressKey('\uE007');
  await check("native Enter keeps focused CTA enabled and prevents a second request", `(() => { const n=document.getElementById('cp-onboarding-add-agent'); n.click(); return document.activeElement===n&&!n.disabled&&n.getAttribute('aria-disabled')==='true'&&fixture.pending.length===1; })()`);
  await session.evaluate("document.getElementById('elsewhere').focus();fixture.finish(0);true"); await wait("!!document.querySelector('dialog[open]')");
  await check("async guide uses native dialog and starts masked", "document.querySelector('dialog') instanceof HTMLDialogElement && document.querySelector('#cp-install-code code').textContent==='' && !document.documentElement.outerHTML.includes('SOURCE-TEST-NOT-EXECUTABLE') && !document.getElementById('cp-onboarding-add-agent').disabled");
  await session.evaluate("fixture.keys=[];true");
  for(let i=0;i<12;i++) await session.pressTab();
  await check("masked guide cycles three times without document-focus loss or stale hidden stops", focused('cp-install-platform-0'));
  for(let i=0;i<12;i++) await shiftTab();
  await check("masked guide reverse cycles preserve native document focus", focused('cp-install-platform-0'));
  await check("boundary Tab is trusted/prevented, interior Tab remains native/unprevented", "fixture.keys.filter(k=>k.key==='Tab').every(k=>k.trusted&&k.hasFocus) && fixture.keys.some(k=>k.key==='Tab'&&k.prevented) && fixture.keys.some(k=>k.key==='Tab'&&!k.prevented)");
  await session.pressTab(); await session.pressKey('\uE007');
  await check("native Enter reveal after cyclic Tab still activates and focuses current Copy", "document.hasFocus() && document.activeElement===document.querySelector('.cp-install-command-actions button:nth-child(2)') && document.querySelector('#cp-install-code code').textContent==='SOURCE-TEST-NOT-EXECUTABLE'");
  await session.pressKey('\uE007'); await wait("!document.querySelector('.cp-install-fallback').hidden");
  await check("native Copy denial reveals/focuses real fallback without clipboard write", "document.hasFocus() && document.activeElement===document.querySelector('.cp-install-fallback')");
  await shiftTab(); await shiftTab();
  await check("reverse from dynamic fallback includes current Hide then Copy", "document.hasFocus() && document.activeElement===document.querySelector('.cp-install-command-actions button:nth-child(2)')");
  await session.pressTab(); await session.pressKey('\uE007');
  await check("native Hide after traversal clears command and fallback and focuses Reveal", focused('cp-install-reveal')+" && !document.querySelector('#cp-install-code code').textContent && document.querySelector('.cp-install-fallback').hidden");
  await shiftTab(); await session.pressKey('\uE014');
  await check("native platform switch changes the selected roving stop without exposing command", focused('cp-install-platform-1')+" && document.getElementById('cp-install-platform-0').tabIndex===-1 && !document.querySelector('#cp-install-code code').textContent");
  for(let i=0;i<4;i++)await session.pressTab();
  await check("Tab boundary recomputes newly selected platform stop", focused('cp-install-platform-1'));
  await shiftTab(); await session.pressKey('\uE007'); await wait("!document.querySelector('dialog')");
  await check("native Close Enter still activates after full dynamic traversal", focused('cp-onboarding-add-agent'));
  await session.pressKey('\uE007'); await session.evaluate("fixture.finish(1);true"); await wait("!!document.querySelector('dialog[open]')");
  await session.pressKey('\uE00C'); await wait("!document.querySelector('dialog')");
  await check("native Escape restores explicit CTA even if focus moved during async loading", "document.activeElement===document.getElementById('cp-onboarding-add-agent')");
  await session.pressKey('\uE007'); await session.pressKey('\uE00C');
  await session.evaluate("fixture.finish(2);true");
  await check("native pending Escape cancels late dialog without forced focus", "!document.querySelector('dialog') && document.activeElement===document.getElementById('cp-onboarding-add-agent') && !document.getElementById('cp-onboarding-add-agent').disabled");
  await session.pressKey('\uE007');
  await session.evaluate("fixture.replacePage();document.getElementById('elsewhere').focus();fixture.finish(3);true");
  await check("teardown signal abort discards async guide and preserves new focus", "fixture.pending[3].signal.aborted && !document.querySelector('dialog') && document.activeElement.id==='elsewhere'");
  await session.evaluate("fixture.helper();document.getElementById('helper-0').focus();fixture.keys=[];true");
  await session.pressTab(); await check("helper interior Tab preserves native movement", focused('helper-1')+" && fixture.keys.at(-1).prevented===false");
  await session.pressTab(); await session.pressTab();
  await check("helper forward boundary excludes disabled/hidden/negative/inert fields", focused('helper-0')+" && fixture.keys.at(-1).prevented===true");
  await shiftTab(); await check("helper reverse boundary wraps first to actual last", focused('helper-2'));
  for(let i=0;i<15;i++)await session.pressTab();
  await shiftTab(); await session.pressKey('\uE007');
  await check("native count Enter activates after five more complete helper cycles", "document.hasFocus() && helperCount===1");
  await session.evaluate("document.getElementById('helper-2').focus();document.getElementById('helper-2').addEventListener('keydown',event=>{if(event.key==='Tab')event.preventDefault();},{once:true});true");
  await session.pressTab(); await check("child-prevented Tab remains at child, helper does not override", focused('helper-2'));
  await session.evaluate("helper.close();helper.dialog.remove();fixture.helper(0);true");
  await session.pressTab(); await shiftTab();
  await check("zero-stop native modal safely keeps document and dialog focus without tabindex", "document.hasFocus() && document.activeElement===helper.dialog && !helper.dialog.hasAttribute('tabindex')");
  await session.pressKey('\uE00C');
  await session.evaluate("helper.dialog.remove();fixture.helper(1,true);true"); await session.pressKey('\uE00C');
  await check("non-dismissable native Escape keeps modal open", "helper.dialog.open && document.hasFocus()");
  await session.evaluate("helper.close();helper.dialog.remove();fixture.helper(1);const inner=fixture.createDialog({titleId:'inner-source-title',title:'Inner'});const button=document.createElement('button');button.id='inner-button';button.type='button';button.textContent='Inner';inner.body.append(button);helper.body.append(inner.dialog);window.innerHelper=inner;inner.open();true");
  await session.pressTab(); await shiftTab();
  await check("nested native modal alone owns both boundaries", focused('inner-button'));
  await session.pressKey('\uE00C'); await wait("!innerHelper.dialog.open");
  await check("nested Escape preserves outer opener restoration", focused('helper-0')+" && helper.dialog.open");
  await session.evaluate("innerHelper.dialog.remove();helper.close();helper.dialog.remove();true");
  await session.evaluate(`(() => {
    fixture.helper(1);
    const sibling=fixture.createDialog({titleId:'sibling-source-title',title:'Topmost sibling'});
    const button=document.createElement('button');button.id='sibling-button';button.type='button';button.textContent='Topmost';sibling.body.append(button);
    window.innerHelper=sibling;sibling.open();return true;
  })()`);
  await session.pressTab(); await shiftTab();
  await check("topmost sibling modal retains boundaries without lower helper interference", focused('sibling-button'));
  await session.pressKey('\uE00C'); await wait("!innerHelper.dialog.open");
  await session.evaluate("innerHelper.dialog.remove();helper.close();helper.dialog.remove();fixture.helper(0);true");
  await session.evaluate(`(() => {
    const fieldset=document.createElement('fieldset');fieldset.disabled=true;
    const legend=document.createElement('legend'),allowed=document.createElement('button');allowed.id='legend-allowed';allowed.type='button';allowed.textContent='Legend control';legend.append(allowed);fieldset.append(legend);
    const excluded=document.createElement('input');excluded.id='fieldset-excluded';fieldset.append(excluded);
    const editable=document.createElement('div');editable.contentEditable='true';editable.id='native-editable';editable.textContent='Editable';
    const first=document.createElement('button');first.id='positive-first';first.tabIndex=1;first.type='button';first.textContent='Positive first';
    const second=document.createElement('button');second.id='positive-second';second.tabIndex=2;second.type='button';second.textContent='Positive second';
    helper.body.append(fieldset,editable,second,first);first.focus();return true;
  })()`);
  await session.pressTab(); await check("native positive tabindex interior order is unchanged", focused('positive-second'));
  await session.evaluate("document.getElementById('native-editable').focus();true");
  await session.pressTab(); await check("forward boundary wraps editable last to positive first", focused('positive-first'));
  await shiftTab(); await check("reverse boundary includes current editable last", focused('native-editable'));
  await session.evaluate("document.getElementById('positive-first').remove();document.getElementById('positive-second').remove();true");
  await session.pressTab(); await check("actual :disabled honors first-legend exception at recalculated boundary", focused('legend-allowed')+" && !document.getElementById('legend-allowed').matches(':disabled') && document.getElementById('fieldset-excluded').matches(':disabled')");
  await session.pressTab(); await check("editable content participates in native sequential order", focused('native-editable'));
  await session.evaluate("helper.close();helper.dialog.remove();true");
  for (const invalid of ["detached", "disabled", "hidden", "inert", "canceled"]) {
    await session.evaluate(`(() => {
      const wrapper=document.createElement('div'), trigger=document.createElement('button');
      trigger.type='button';wrapper.append(trigger);document.body.append(wrapper);
      document.getElementById('elsewhere').focus();
      let current=true;
      const modal=fixture.createDialog({titleId:'source-dialog-title',title:'Source test'});
      modal.open({restoreFocusTo:trigger,shouldRestoreFocus:()=>current});
      if('${invalid}'==='detached')trigger.remove();
      if('${invalid}'==='disabled')trigger.disabled=true;
      if('${invalid}'==='hidden')wrapper.hidden=true;
      if('${invalid}'==='inert')wrapper.inert=true;
      if('${invalid}'==='canceled')current=false;
      window.dialogTest={modal,wrapper};modal.close();return true;
    })()`);
    await wait("!dialogTest.modal.dialog.open");
    await check(`real dialog ignores ${invalid} explicit trigger`, "document.activeElement.id==='elsewhere'");
    await session.evaluate("dialogTest.modal.dialog.remove();dialogTest.wrapper.remove();true");
  }
  await session.evaluate(`(() => {
    document.getElementById('elsewhere').focus();
    const modal=fixture.createDialog({titleId:'source-default-dialog-title',title:'Default source test'});
    window.dialogTest={modal};modal.open();modal.close();return true;
  })()`);
  await wait("!dialogTest.modal.dialog.open");
  await check("default dialogs keep existing active-element restoration", "document.activeElement.id==='elsewhere'");
  await session.evaluate("dialogTest.modal.dialog.remove();true");
  }
  assert.equal(session.javascriptErrors.length, 0); assert.equal(session.networkFailures.length, 0);
  assert.equal(report.blockedRequests.length,0);
  assert.equal(traffic.some(item=>item.path.startsWith('/api/')||item.method!=="GET"),false);
  report.trustedKeys=await session.evaluateJSON("fixture.keys");
  report.status="passed";
} catch (error) { report.status="failed"; report.error=String(error); process.exitCode=1; }
finally {
  await session.stop();
  report.browser={ stopped: !session.firefox?.pid || session.firefox.exitCode!==null || session.firefox.signalCode!==null, profileRemoved: !session.profile || !existsSync(session.profile) };
  server.closeAllConnections(); await new Promise(done=>server.close(done));
  report.traffic=traffic; report.capabilities=session.capabilities;
  writeFileSync(resolve(out,"report.json"),JSON.stringify(report,null,2)+"\n",{mode:0o600});
  console.log(`${report.status}: ${report.checks.filter(check=>check.pass).length}/${report.checks.length} A source DOM checks; profile removed=${report.browser.profileRemoved}`);
  if(report.error)console.error(report.error);
}
