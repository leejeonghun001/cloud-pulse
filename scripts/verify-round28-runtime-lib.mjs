// Pure QA contracts: no browser/server/filesystem side effects.
import { formatDisplayAmount } from '../web/assets/js/core/currency.js';

export function compareRealBilling(real, ui) {
  const errors=[];
  const body=real?.body;
  if(real?.status!==200)errors.push('billing GET must return HTTP 200');
  if(!body||typeof body!=='object'||Array.isArray(body))return {pass:false,status:real?.status??null,snapshotCount:null,hostCount:null,errors:[...errors,'billing body must be an object']};
  const typedList=(key)=>{
    if(!Object.hasOwn(body,key)||body[key]!==null&&!Array.isArray(body[key])) {errors.push(`${key} must be a present array or Go typed-nil null`);return null;}
    return body[key]===null?[]:body[key];
  };
  const snapshots=typedList('snapshots'),hosts=typedList('hosts');
  for(const [index,s] of (snapshots??[]).entries())if(!s||typeof s.provider!=='string'||!s.provider||typeof s.status!=='string'||!Number.isFinite(s.mtd_cost)||!Number.isFinite(s.forecast_cost))errors.push(`invalid real API snapshot at ${index}`);
  const currency=body.display_currency;
  if(!currency||Array.isArray(currency)||!['USD','KRW'].includes(currency.currency)||currency.currency==='KRW'&&(!Number.isFinite(currency.krw_per_usd)||currency.krw_per_usd<=0))errors.push('invalid display currency contract');
  if(!ui||!Array.isArray(ui.rows))errors.push('rendered row proof must be an array');
  else if(hosts) {
    if(ui.rows.length!==hosts.length)errors.push('rendered/API host count mismatch');
    if(hosts.length===0&&ui.emptyVisible!==true)errors.push('zero hosts require the confirmed localized empty state, not a vacuous row comparison');
    const seen=new Set();
    for(const [index,h] of hosts.entries()) {
      if(!h||typeof h.host_id!=='string'||!h.host_id||seen.has(h.host_id)||!Number.isFinite(h.total_mtd)||!Number.isFinite(h.total_forecast)) {errors.push(`invalid real API host totals/identity at ${index}`);continue;}
      seen.add(h.host_id);
      const row=ui.rows[index];
      const expected=[h.total_mtd,h.total_forecast].map(n=>formatDisplayAmount(n,currency,{taxNote:false}));
      if(!row||row.href!==`#/host/${encodeURIComponent(h.host_id)}`||JSON.stringify(row.values)!==JSON.stringify(expected))errors.push(`exact rendered/API totals mismatch at ${index}`);
    }
  }
  return {pass:errors.length===0,status:real?.status??null,snapshotCount:snapshots?.length??null,snapshotsKind:body.snapshots===null?'typed-nil':Array.isArray(body.snapshots)?'array':'invalid',hostCount:hosts?.length??null,emptyVisible:ui?.emptyVisible===true,totals:ui?.rows??null,errors};
}

// Serialized into the browser guard and also directly unit-tested. Only the
// two real settings PUT contracts are matched; no POST/refresh/config routes.
export function billingSaveStub(fixture,method,path,body) {
  if(fixture.state!=='billing-fixture'||method!=='PUT'||!['/api/v1/settings/billing/currency','/api/v1/settings/billing/interval'].includes(path))return null;
  const invalid=(error,code='invalid_request_body')=>({status:400,body:{error,code}});
  if(!body||typeof body!=='object'||Array.isArray(body))return invalid('invalid request body');
  if(path.endsWith('/interval')) {
    if(Object.keys(body).some(k=>k!=='interval')||!['6h','12h','24h'].includes(body.interval))return invalid('interval must be one of 6h, 12h, 24h','invalid_interval');
  } else {
    if(Object.keys(body).some(k=>!['currency','krw_per_usd'].includes(k))||!['USD','KRW'].includes(body.currency))return invalid('invalid display currency');
    if(body.currency==='KRW'&&(!Number.isFinite(body.krw_per_usd)||body.krw_per_usd<=0))return invalid('KRW requires a positive rate','rate_required');
  }
  if(fixture.billingSaveMode==='server-error')return {status:500,body:{error:'internal server error',code:'internal_error'}};
  if(path.endsWith('/interval')) {
    fixture.billing.interval_seconds=Number(body.interval.slice(0,-1))*3600;
    return {status:200,body:{interval:body.interval}};
  }
  const before=fixture.billing.display_currency;
  const after={...before,currency:body.currency};
  if(body.currency==='KRW'&&body.krw_per_usd!==before.krw_per_usd) {after.krw_per_usd=body.krw_per_usd;after.rate_updated_at=fixture.billingSaveAt;}
  fixture.billing.display_currency=after;
  return {status:200,body:after};
}

export function firefoxMotionPreference(reducedMotion) {
  if(typeof reducedMotion!=='boolean')throw new Error('reducedMotion must be boolean');
  return `user_pref("ui.prefersReducedMotion", ${reducedMotion?1:0});\n`;
}

export function reducedAnimationsAreQuiet(animations) {
  // Web Animations uses milliseconds; gallery's near-zero ceiling is 0.01s.
  return Array.isArray(animations)&&animations.every(a=>a.state!=='running'||Number.isFinite(a.duration)&&a.duration<=10&&Number.isFinite(a.iterations)&&a.iterations<=1);
}

export async function activateAndPress(session,value) {
  // Native Tab traversal can legitimately leave Firefox page focus for its
  // browser chrome. Restore browsing-context activation only before an
  // explicitly targeted action; NEVER insert this into the 14-Tab trace.
  await session.send('browsingContext.activate',{context:session.context});
  const focus=await session.evaluateJSON(`(()=>{
    window.__round28IntendedKeyTarget=document.activeElement;
    if(document.hasFocus())return {pageHasFocus:true};
    const modal=document.querySelector('dialog[open]'),scope=modal||document.querySelector('main')||document.body;
    for(const node of scope.querySelectorAll('h1,h2,h3,p,section')) {
      const r=node.getBoundingClientRect();if(r.width<=0||r.height<=0)continue;
      const x=Math.max(2,Math.min(innerWidth-2,r.left+r.width/2)),y=Math.max(2,Math.min(innerHeight-2,r.top+Math.min(r.height/2,8)));
      if(y<r.top||y>r.bottom)continue;
      const hit=document.elementFromPoint(x,y);
      if(hit&&(!modal||modal.contains(hit))&&!hit.closest('a,button,input,select,textarea,label,[role=button],[contenteditable],input[type=file]'))return {pageHasFocus:false,pointer:{x:Math.round(x),y:Math.round(y)}};
    }
    return {pageHasFocus:false,pointer:null};
  })()`);
  if(!focus.pageHasFocus) {
    if(!focus.pointer)throw new Error('Cannot safely focus Firefox content: no neutral non-action pointer target');
    await session.send('input.performActions',{context:session.context,actions:[{type:'pointer',id:'round28-content-focus',parameters:{pointerType:'mouse'},actions:[{type:'pointerMove',x:focus.pointer.x,y:focus.pointer.y,origin:'viewport'},{type:'pointerDown',button:0},{type:'pointerUp',button:0}]}]});
    await session.send('input.releaseActions',{context:session.context});
  }
  const restored=await session.evaluate(`(()=>{const n=window.__round28IntendedKeyTarget;delete window.__round28IntendedKeyTarget;if(n?.isConnected)n.focus({preventScroll:true});return document.hasFocus()&&document.activeElement===n;})()`);
  if(!restored)throw new Error('Firefox content focus/intended native-key target could not be restored');
  return session.pressKey(value);
}

export function scrollCaptureTargetExpression(selector) {
  return `(()=>{const n=document.querySelector(${JSON.stringify(selector)});if(!n)return false;n.scrollIntoView({block:'start'});
    if(!n.closest('dialog[open]')) {const header=document.querySelector('.cp-shell-header'),h=header?.getBoundingClientRect(),r=n.getBoundingClientRect();
      if(h&&r.top<h.bottom+12)window.scrollBy({top:r.top-h.bottom-12,behavior:'instant'});}
    return true;})()`;
}
