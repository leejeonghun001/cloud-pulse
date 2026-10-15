// Focused native-input proof. No recovery, synthetic key dispatch or product edits.
import { readFileSync,readlinkSync,existsSync,lstatSync } from 'node:fs';
import { createHash } from 'node:crypto';
import { execFileSync } from 'node:child_process';
import { join } from 'node:path';
import { REPO_ROOT,safeVerificationPath } from './verify-round28-safety.mjs';

export function focusedInventoryHash(inventory) {
  return createHash('sha256').update(JSON.stringify(inventory.map(row=>({path:row.path,mode:row.mode,sha256:row.sha256})))).digest('hex');
}

export function validateFrozenProofSource(manifestPath) {
  const source=JSON.parse(readFileSync(safeVerificationPath(manifestPath),'utf8'));
  if(!Array.isArray(source.input_inventory)||!source.input_inventory.length)throw new Error('Missing focused source inventory');
  const tree=focusedInventoryHash(source.input_inventory);
  if(tree!==source.tree_sha256)throw new Error('Focused source fingerprint is invalid');
  if(source.head!==execFileSync('git',['rev-parse','HEAD'],{cwd:REPO_ROOT,encoding:'utf8'}).trim())throw new Error('Focused source HEAD drift');
  const current=execFileSync('git',['ls-files','--cached','--others','--exclude-standard','-z'],{cwd:REPO_ROOT,encoding:'utf8'}).split('\0').filter(p=>p&&existsSync(join(REPO_ROOT,p)));
  if(JSON.stringify([...new Set(current)].sort())!==JSON.stringify(source.input_inventory.map(row=>row.path).sort()))throw new Error('Focused source input set drift');
  for(const row of source.input_inventory) {
    if(!row.path||row.path.startsWith('/')||row.path.split('/').includes('..'))throw new Error('Unsafe source inventory path');
    const path=join(REPO_ROOT,row.path);
    const stat=lstatSync(path),mode=stat.isSymbolicLink()?'120000':stat.mode&0o111?'100755':'100644';
    if(mode!==row.mode)throw new Error(`Focused source mode drift: ${row.path}`);
    const hash=createHash('sha256').update(row.mode==='120000'?readlinkSync(path):readFileSync(path)).digest('hex');
    if(hash!==row.sha256)throw new Error(`Focused source drift: ${row.path}`);
  }
  return {head:source.head,tree_sha256:tree,input_count:source.input_inventory.length};
}

export function validateNativeWitness({before,after,events,expected,outcome}) {
  const errors=[];
  if(!before?.hasFocus||!after?.hasFocus)errors.push('document content focus lost');
  if(!before?.targetVisible||before?.targetDisabled||before?.targetFile)errors.push('intended control not safely usable');
  const down=events?.filter(e=>e.type==='keydown'&&e.phase==='after-handlers'&&e.key===expected.key)??[];
  const up=events?.filter(e=>e.type==='keyup'&&e.phase==='after-handlers'&&e.key===expected.key)??[];
  for(const [kind,list] of [['keydown',down],['keyup',up]]) {
    if(list.length!==1)errors.push(`exactly one ${kind} witness required`);
    else if(!list[0].trusted||!list[0].hasFocus||!(kind==='keydown'?list[0].targetExpected:list[0].targetIsActive)||list[0].code!==expected.code||list[0].keyCode!==expected.keyCode||list[0].shift!==Boolean(expected.shift))errors.push(`wrong/untrusted ${kind} target, focus or key`);
  }
  if(!outcome)errors.push('expected native UI outcome absent');
  return {pass:errors.length===0,errors};
}

export function validateFocusCycle(steps,direction) {
  if(!Array.isArray(steps)||steps.length!==14)return {pass:false,errors:['exactly 14 native steps required']};
  const errors=[];let wraps=0;
  for(const step of steps) {
    if(!step.witness?.pass||!step.after?.hasFocus||!step.after?.inDialog||!step.after?.targetInView)errors.push('native key/focus/modal containment or visible focused rectangle failed');
    if(step.before?.atBoundary) {
      wraps++;
      if(step.after?.active!==step.before?.wrapTarget||!step.events.some(e=>e.type==='keydown'&&e.phase==='after-handlers'&&e.key==='Tab'&&e.defaultPrevented))errors.push('boundary did not wrap/prevent default');
    }
  }
  if(!wraps)errors.push('no actual boundary wrap observed');
  return {pass:errors.length===0,direction,wraps,errors};
}

const q=JSON.stringify,sleep=ms=>new Promise(done=>setTimeout(done,ms));
async function wait(session,expression,label) {for(let i=0;i<150;i++){if(await session.evaluate(expression))return;await sleep(50);}throw new Error(`Focused proof timed out: ${label}`);}
export function nativeTraceExpression() {
  return `(()=>{
    window.__round28NativeTrace=[];
    for(const type of ['keydown','keyup'])window.addEventListener(type,e=>{
      const target=e.target,expected=window.__round28NativeTarget;
      window.__round28NativeTrace.push({type,phase:'after-handlers',key:e.key,code:e.code,keyCode:e.keyCode,trusted:e.isTrusted,defaultPrevented:e.defaultPrevented,shift:e.shiftKey,targetExpected:target===expected,targetIsActive:target===document.activeElement,target:{tag:target?.tagName,id:target?.id||''},hasFocus:document.hasFocus()});
    });
    return true;
  })()`;
}
async function focusedState(session,targetSelector,shift=false) {
  return session.evaluateJSON(`(()=>{
    const target=${targetSelector?`document.querySelector(${q(targetSelector)})`:'document.activeElement'};
    const modal=document.querySelector('dialog[open]');
    const candidates=modal?[...modal.querySelectorAll('a[href],button,input,select,textarea,[tabindex]')].filter(n=>{const r=n.getBoundingClientRect(),s=getComputedStyle(n);return n.tabIndex>=0&&!n.matches(':disabled')&&!n.closest('[hidden],[inert]')&&r.width>0&&r.height>0&&s.visibility!=='hidden';}):[];
    const identify=n=>n?Array.from(document.querySelectorAll('*')).indexOf(n):null;
    const index=candidates.indexOf(target),next=candidates.length?candidates[(index+(${shift?-1:1})+candidates.length)%candidates.length]:null;
    const r=target?.getBoundingClientRect(),s=target?getComputedStyle(target):null;
    const m=modal?.getBoundingClientRect(),port=modal?{left:Math.max(0,m.left+modal.clientLeft),top:Math.max(0,m.top+modal.clientTop),right:Math.min(innerWidth,m.left+modal.clientLeft+modal.clientWidth),bottom:Math.min(innerHeight,m.top+modal.clientTop+modal.clientHeight)}:{left:0,top:0,right:innerWidth,bottom:innerHeight};
    const targetInView=Boolean(r&&r.width>0&&r.height>0&&r.left>=port.left-1&&r.right<=port.right+1&&r.top>=port.top-1&&r.bottom<=port.bottom+1);
    window.__round28NativeTarget=target;
    return {hasFocus:document.hasFocus(),active:identify(document.activeElement),target:identify(target),targetId:target?.id||'',targetVisible:Boolean(r&&r.width>0&&r.height>0&&s.visibility!=='hidden'&&!target.closest('[hidden],[inert]')),targetInView,targetRect:r?{top:r.top,bottom:r.bottom,left:r.left,right:r.right}:null,visiblePort:port,dialogScroll:modal?.scrollTop??null,targetDisabled:target?.matches(':disabled')||false,targetFile:target?.type==='file',inDialog:Boolean(modal?.contains(document.activeElement)),atBoundary:${shift?'index===0':'index===candidates.length-1'}&&index>=0,wrapTarget:identify(${shift?'candidates.at(-1)':'candidates[0]'}),nextTarget:identify(next),candidateIds:candidates.map(n=>({id:n.id,tag:n.tagName,index:identify(n)})),masked:!document.querySelector('#cp-install-code code')?.textContent&&!document.querySelector('.cp-install-fallback')?.value,upload:Boolean(window.__round28UploadObserved)||[...document.querySelectorAll('input[type=file]')].some(n=>n.files?.length)};
  })()`);
}
async function rawKey(session,value,shift) {
  if(!shift)return session.pressKey(value);
  await session.send('input.performActions',{context:session.context,actions:[{type:'key',id:'keyboard',actions:[{type:'keyDown',value:'\uE008'},{type:'keyDown',value},{type:'keyUp',value},{type:'keyUp',value:'\uE008'}]}]});
  await session.send('input.releaseActions',{context:session.context});
}
async function initialPointer(session,selector) {
  const point=await session.evaluateJSON(`(()=>{const n=document.querySelector(${q(selector)});if(!n)return null;n.scrollIntoView({block:'center'});const r=n.getBoundingClientRect(),x=Math.round(r.left+r.width/2),y=Math.round(r.top+r.height/2),hit=document.elementFromPoint(x,y);return {x,y,hitMatches:hit===n||n.contains(hit),disabled:n.matches(':disabled'),file:n.type==='file'};})()`);
  if(!point?.hitMatches||point.disabled||point.file)throw new Error('Focused proof unsafe initial native pointer target');
  await session.send('input.performActions',{context:session.context,actions:[{type:'pointer',id:'focused-initial-pointer',parameters:{pointerType:'mouse'},actions:[{type:'pointerMove',origin:'viewport',x:point.x,y:point.y},{type:'pointerDown',button:0},{type:'pointerUp',button:0}]}]});
  await session.send('input.releaseActions',{context:session.context});
  return point;
}

export async function runFocusedNativeProof(session,{hub,guard,reducedMotion,report}) {
  const proof={motion:reducedMotion?'reduce':'normal',context:session.context,checks:[],actions:[],routes:[],initialPointer:null,status:'running',focusRecoveryPerformed:false};
  report.focusedProfiles.push(proof);
  const check=(name,pass,detail)=>{proof.checks.push({name,pass:Boolean(pass),detail});if(!pass)throw new Error(`Focused native proof failed: ${name}`);};
  const catalogs=JSON.parse(readFileSync(join(REPO_ROOT,'web/assets/i18n/en.json'),'utf8'));
  async function open(route,state,ready) {
    if(!await session.evaluate('document.hasFocus()'))throw new Error('Focused proof refuses navigation recovery after document focus loss');
    const context=session.context;
    const preload=await session.send('script.addPreloadScript',{functionDeclaration:`()=>{${guard(hub,state)};${nativeTraceExpression()};}`});
    try {await session.navigate(`${hub}/?round28_focused=${proof.routes.length}${route}`);}finally{await session.send('script.removePreloadScript',{script:preload.script});}
    await wait(session,ready,route);
    check(`${route}: same browser context and actual content focus`,session.context===context&&await session.evaluate('document.hasFocus()'));
    proof.routes.push({route,state,context:session.context});
  }
  async function key(label,{value,key,code,keyCode,selector,shift=false,outcome}) {
    if(selector)await session.evaluate(`(()=>{const n=document.querySelector(${q(selector)});if(!document.hasFocus()||!n)throw new Error('No focused native target');n.scrollIntoView({block:'nearest'});n.focus({preventScroll:true});return true;})()`);
    const before=await focusedState(session,selector,shift);
    if(before.upload)throw new Error('STOP: original/source file upload observed');
    if(!before.hasFocus||before.active!==before.target||!before.masked||!before.targetInView)throw new Error(`Focused proof refuses key without actual visible target/content focus/masking: ${label}`);
    const cursor=await session.evaluate('window.__round28NativeTrace.length');
    await rawKey(session,value,shift);await sleep(40);
    // Reading state must not replace the expected target before event collection.
    const events=await session.evaluateJSON(`window.__round28NativeTrace.slice(${cursor})`);
    let after=await focusedState(session,null,shift);
    if(after.upload)throw new Error('STOP: original/source file upload observed');
    const uiOutcome=await outcome({before,after,events});
    after=await focusedState(session,null,shift);
    const witness=validateNativeWitness({before,after,events,expected:{key,code,keyCode,shift},outcome:uiOutcome});
    const action={label,before,after,events,witness};proof.actions.push(action);
    check(label,witness.pass,witness);return action;
  }
  try {
    await session.evaluate(nativeTraceExpression());
    // Only this initial pointer establishes content focus. No recovery inside
    // cycles or later same-context actions; native outcomes must remain usable.
    proof.initialPointer=await initialPointer(session,'#cp-onboarding-add-agent');
    await wait(session,"Boolean(document.querySelector('.cp-add-agent-dialog[open]'))",'setup pointer dialog');
    check('actual Firefox motion preference',await session.evaluate(`matchMedia('(prefers-reduced-motion: reduce)').matches===${reducedMotion}`));
    // Pointer click is merely setup and may open the dialog; close natively first.
    if(await session.evaluate("Boolean(document.querySelector('.cp-add-agent-dialog[open]'))"))await key('setup Escape',{value:'\uE00C',key:'Escape',code:'Escape',keyCode:27,outcome:async()=>await session.evaluate("!document.querySelector('.cp-add-agent-dialog')&&document.activeElement.id==='cp-onboarding-add-agent'")});
    await key('native Enter opens masked Add agent',{value:'\uE007',key:'Enter',code:'NumpadEnter',keyCode:13,selector:'#cp-onboarding-add-agent',outcome:async()=>{await wait(session,"Boolean(document.querySelector('.cp-add-agent-dialog[open]'))",'Add agent open');return await session.evaluate("document.activeElement.id==='cp-install-platform-0'&&!document.querySelector('#cp-install-code code').textContent&&!document.querySelector('.cp-install-fallback').value");}});
    for(const shift of [false,true]) {
      const steps=[];
      for(let index=0;index<14;index++)steps.push(await key(`${shift?'ShiftTab':'Tab'} ${index+1}`,{value:'\uE004',key:'Tab',code:'Tab',keyCode:9,shift,outcome:async({before,after})=>after.hasFocus&&after.inDialog&&after.masked&&after.targetInView&&after.active===before.nextTarget}));
      const cycle=validateFocusCycle(steps,shift?'backward':'forward');check(`${shift?'backward':'forward'} boundary wrapping, no recovery`,cycle.pass,cycle);
    }
    await key('native Escape clears guide and restores connected CTA',{value:'\uE00C',key:'Escape',code:'Escape',keyCode:27,outcome:async()=>{await wait(session,"!document.querySelector('.cp-add-agent-dialog')",'guide cleanup');return await session.evaluate("document.activeElement.id==='cp-onboarding-add-agent'&&document.activeElement.isConnected&&!document.querySelector('#cp-install-code')&&!document.querySelector('.cp-install-fallback')&&!document.documentElement.outerHTML.includes('phase1-local-agent-token-0123456789')");}});
    await open('#/settings/notifications','notification-runtime',"Boolean(document.querySelector('.cp-channel-row'))");
    await key('same-context native Enter opens notification edit',{value:'\uE007',key:'Enter',code:'NumpadEnter',keyCode:13,selector:'.cp-channel-row-actions button:nth-child(2)',outcome:async()=>{await wait(session,"Boolean(document.querySelector('.cp-notify-dialog[open]'))",'edit open');return await session.evaluate("document.getElementById('cp-channel-name')!==null&&document.getElementById('cp-channel-field-bot_token').value===''&&!document.documentElement.outerHTML.includes('round28-private-fixture-not-real')");}});
    await key('same-context native Escape closes notification edit',{value:'\uE00C',key:'Escape',code:'Escape',keyCode:27,outcome:async()=>{await wait(session,"!document.querySelector('.cp-notify-dialog')",'edit close');return await session.evaluate("document.activeElement===document.querySelector('.cp-channel-row-actions button:nth-child(2)')&&window.__round28CaptureProof.notificationTests.length===0");}});
    await open('#/settings/billing','billing-fixture',"Boolean(document.getElementById('cp-billing-save-currency'))");
    // Input value is test data preparation, not a synthetic keyboard action.
    await session.evaluate("document.getElementById('cp-billing-krw-rate').value='0';true");
    await key('same-context native billing invalid validation',{value:'\uE007',key:'Enter',code:'NumpadEnter',keyCode:13,selector:'#cp-billing-krw-rate',outcome:async()=>await session.evaluate("document.activeElement.id==='cp-billing-krw-rate'&&document.getElementById('cp-billing-krw-rate').getAttribute('aria-invalid')==='true'&&document.getElementById('cp-billing-currency-status').textContent==="+q(catalogs.billing.enter_positive_rate)+"&&window.__round28CaptureProof.billingWrites.length===0")});
    await open('#/alerts','read-only',"document.querySelectorAll('.cp-activity-event').length===50&&document.querySelector('.cp-activity-card').getAttribute('aria-busy')==='false'");
    await key('same-context native real-history Load more',{value:'\uE007',key:'Enter',code:'NumpadEnter',keyCode:13,selector:'#cp-activity-load-more',outcome:async()=>{await wait(session,"document.querySelectorAll('.cp-activity-event').length===55",'55 real records');return await session.evaluate("!document.getElementById('cp-activity-load-more')&&window.__round28CaptureProof.realReads.some(r=>r.path==='/api/v1/alerts/events'&&r.query.includes('before='))");}});
    await key('same-context native real-history filter',{value:'\uE015',key:'ArrowDown',code:'ArrowDown',keyCode:40,selector:'#cp-alerts-state-filter',outcome:async()=>{await wait(session,"document.getElementById('cp-alerts-state-filter').value==='firing'&&document.querySelector('.cp-activity-card').getAttribute('aria-busy')==='false'",'firing filter');return await session.evaluate("document.querySelectorAll('.cp-activity-event').length===14&&window.__round28CaptureProof.realReads.some(r=>r.path==='/api/v1/alerts/events'&&r.query.includes('state=firing'))");}});
    check('same context retained throughout proof',proof.routes.every(r=>r.context===proof.context));
    check('strict headings, console/CSP/assets, masking and no writes',await session.evaluate("document.hasFocus()&&!window.__round28UploadObserved&&!window.__verificationCSP.length&&!window.__verificationResourceErrors.length&&window.__round28CaptureProof.blocked.length===0&&[...document.querySelectorAll('[aria-labelledby]')].every(n=>n.getAttribute('aria-labelledby').split(/\\s+/).every(id=>document.getElementById(id)?.textContent.trim()))")&&!session.consoleErrors.length&&!session.javascriptErrors.length&&!session.networkFailures.length);
    proof.status='passed';
  } catch(error) {proof.status='failed';proof.failure=String(error.stack||error);report.failures.push(proof.failure);if(/STOP:/.test(error.message))throw error;}
  return proof;
}
