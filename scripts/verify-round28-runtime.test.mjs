import test from 'node:test';
import assert from 'node:assert/strict';
import { Script } from 'node:vm';
import { compareRealBilling,billingSaveStub,firefoxMotionPreference,reducedAnimationsAreQuiet,activateAndPress,scrollCaptureTargetExpression } from './verify-round28-runtime-lib.mjs';
import { BILLING_FIXTURE,HEALTH_FIXTURE } from './verify-round28-runtime.mjs';
import { installGuardExpression,requestPolicy } from './verify-round28-firstimpression.mjs';
import { BidiSession } from './verify-phase1-auth.mjs';

const host={host_id:'fixture/one',total_mtd:12.34,total_forecast:-56.78};
const row={href:'#/host/fixture%2Fone',values:['$12.34','$-56.78']};
const body=snapshots=>({snapshots,hosts:[host],display_currency:{currency:'USD'}});

test('real billing typed-nil, empty and populated snapshot arrays are counted without hiding exact totals',()=>{
  for(const [snapshots,count] of [[null,0],[[],0],[BILLING_FIXTURE.snapshots,2]]) {
    const result=compareRealBilling({status:200,body:body(snapshots)},{rows:[row],emptyVisible:false});
    assert.equal(result.pass,true,JSON.stringify(result));assert.equal(result.snapshotCount,count);assert.equal(result.hostCount,1);
  }
});
test('real billing invalid/missing typed-list fields and HTTP failure cannot false-pass',()=>{
  for(const snapshots of [undefined,{},'null',[{}]])assert.equal(compareRealBilling({status:200,body:body(snapshots)},{rows:[row]}).pass,false);
  const missing=body(null);delete missing.snapshots;
  assert.equal(compareRealBilling({status:200,body:missing},{rows:[row]}).pass,false);
  assert.equal(compareRealBilling({status:500,body:body(null)},{rows:[row]}).pass,false);
  assert.equal(compareRealBilling({status:200,body:null},{rows:[]}).pass,false);
});
test('real billing requires exact row identity, count and numeric API totals',()=>{
  for(const rows of [[],[row,row],[{...row,href:'#/host/wrong'}],[{...row,values:['$0.00','$-56.78']}]])assert.equal(compareRealBilling({status:200,body:body(null)},{rows}).pass,false);
  for(const total of [null,'12.34',NaN,Infinity])assert.equal(compareRealBilling({status:200,body:{...body(null),hosts:[{...host,total_mtd:total}]}},{rows:[row]}).pass,false);
});
test('nil/empty hosts require confirmed visible empty UI, never a vacuous comparison',()=>{
  for(const hosts of [null,[]]) {
    const real={status:200,body:{...body(null),hosts}};
    assert.equal(compareRealBilling(real,{rows:[],emptyVisible:false}).pass,false);
    assert.equal(compareRealBilling(real,{rows:[],emptyVisible:true}).pass,true);
    assert.equal(compareRealBilling(real,{rows:[row],emptyVisible:true}).pass,false);
  }
});
const fixture=()=>({state:'billing-fixture',billingSaveMode:'success',billingSaveAt:1780128001,billing:structuredClone(BILLING_FIXTURE)});
test('billing save stubs match real currency/interval PUT response shape and memory-only reload state',()=>{
  const f=fixture();
  const saved=billingSaveStub(f,'PUT','/api/v1/settings/billing/currency',{currency:'KRW',krw_per_usd:1400.25});
  assert.deepEqual(saved,{status:200,body:{currency:'KRW',krw_per_usd:1400.25,rate_updated_at:1780128001}});
  assert.deepEqual(f.billing.display_currency,saved.body);
  assert.deepEqual(billingSaveStub(f,'PUT','/api/v1/settings/billing/interval',{interval:'6h'}),{status:200,body:{interval:'6h'}});
  assert.equal(f.billing.interval_seconds,21600);
  const usd=billingSaveStub(f,'PUT','/api/v1/settings/billing/currency',{currency:'USD',krw_per_usd:0});
  assert.equal(usd.body.krw_per_usd,1400.25);assert.equal(usd.body.rate_updated_at,1780128001);
});
test('billing save errors do not mutate fixture settings; unmatched methods/routes stay denied',()=>{
  const f=fixture(),before=JSON.stringify(f.billing);f.billingSaveMode='server-error';
  assert.deepEqual(billingSaveStub(f,'PUT','/api/v1/settings/billing/currency',{currency:'KRW',krw_per_usd:1400.25}),{status:500,body:{error:'internal server error',code:'internal_error'}});
  assert.equal(JSON.stringify(f.billing),before);
  assert.equal(billingSaveStub(f,'PUT','/api/v1/settings/billing/currency',{currency:'KRW',krw_per_usd:0}).body.code,'rate_required');
  for(const [method,path] of [['POST','/api/v1/settings/billing/currency'],['POST','/api/v1/billing/refresh'],['PUT','/api/v1/alerts/channels/9001'],['PUT','/api/v1/settings/provider']])assert.equal(billingSaveStub(f,method,path,{}),null);
  assert.equal(billingSaveStub({...f,state:'read-only'},'PUT','/api/v1/settings/billing/interval',{interval:'6h'}),null);
  assert.equal(JSON.stringify(f.billing),before);
});
function guardContext(state) {
  const actualCalls=[];
  const context={window:{fetch:async(...args)=>{actualCalls.push(args);return Response.json({fixtureRead:true});}},document:{querySelectorAll:()=>[],addEventListener(){}},location:{href:'http://127.0.0.1:18190/'},URL,Response,FormData,File};
  new Script(installGuardExpression('http://127.0.0.1:18190',state)).runInNewContext(context);
  return {context,actualCalls};
}
test('actual serialized browser guard intercepts save success/error before original fetch and still refuses unsafe writes/outbound/upload',async()=>{
  const {context,actualCalls}=guardContext('billing-fixture'),fetch=context.window.fetch;
  const success=await fetch('/api/v1/settings/billing/currency',{method:'PUT',body:JSON.stringify({currency:'KRW',krw_per_usd:1400.25})});
  assert.equal(success.status,200);assert.equal((await success.json()).krw_per_usd,1400.25);
  context.window.__round28Fixture.billingSaveMode='server-error';
  assert.equal((await fetch('/api/v1/settings/billing/interval',{method:'PUT',body:'{"interval":"6h"}'})).status,500);
  assert.equal(actualCalls.length,0);assert.equal(context.window.__round28CaptureProof.billingWrites.length,2);
  for(const [url,method] of [['/api/v1/billing/refresh','POST'],['/api/v1/alerts/channels/9001/synthetic','POST'],['https://api.telegram.org/send','GET'],['/api/v1/settings/provider','PUT']])await assert.rejects(fetch(url,{method}),/Round28 blocked/);
  assert.equal(actualCalls.length,0);
  context.window.__round28UploadObserved=true;await assert.rejects(fetch('/api/v1/hosts'),/STOP: original\/source file upload observed/);
  assert.equal(actualCalls.length,0);
  assert.equal(requestPolicy('/api/v1/settings/billing/currency','PUT'),'blocked-write','BiDi backstop must fail any PUT that escapes the in-page fixture');
});
test('Health fixture is complete metadata with disabled schedule and zero attempts, not fabricated delivery evidence',()=>{
  const ch=HEALTH_FIXTURE.channels[0];
  assert.equal(ch.channel_name,'Isolated documentation fixture');assert.equal(ch.state,'unknown');assert.equal(ch.enabled,false);assert.equal(ch.settings.enabled,false);
  assert.equal(ch.stats_7d.total,0);assert.equal(ch.stats_30d.total,0);assert.deepEqual(ch.recent_synthetic,[]);assert.equal(ch.last_synthetic,undefined);
});
test('Firefox reduced-motion uses the existing gallery native preference approach without starting a process',()=>{
  assert.equal(firefoxMotionPreference(true),'user_pref("ui.prefersReducedMotion", 1);\n');
  assert.equal(firefoxMotionPreference(false),'user_pref("ui.prefersReducedMotion", 0);\n');
  assert.throws(()=>firefoxMotionPreference('true'),/boolean/);
  const session=new BidiSession({reducedMotion:true});assert.equal(session.reducedMotion,true);assert.equal(session.firefox,null);assert.equal(session.profile,null);
});
test('reduced-motion timing uses gallery units and cannot accept null/Infinity iterations as finite',()=>{
  assert.equal(reducedAnimationsAreQuiet([]),true);
  assert.equal(reducedAnimationsAreQuiet([{state:'running',duration:0.001,iterations:1}]),true);
  for(const iterations of [Infinity,null,'infinite',2])assert.equal(reducedAnimationsAreQuiet([{state:'running',duration:0.001,iterations}]),false);
  assert.equal(reducedAnimationsAreQuiet([{state:'running',duration:100,iterations:1}]),false);
});
test('explicit native actions reactivate the browsing context before sending keys, without modifying Tab traces',async()=>{
  const calls=[],session={context:'owned-fixture-context',send:async(method,params)=>calls.push({method,params}),evaluateJSON:async()=>({pageHasFocus:true}),evaluate:async()=>true,pressKey:async value=>calls.push({key:value})};
  await activateAndPress(session,'\uE007');
  assert.deepEqual(calls,[{method:'browsingContext.activate',params:{context:'owned-fixture-context'}},{key:'\uE007'}]);
});
test('Firefox chrome focus recovery clicks only a supplied neutral content point and verifies intended focus before key',async()=>{
  const calls=[],session={context:'owned-fixture-context',send:async(method,params)=>calls.push({method,params}),evaluateJSON:async()=>({pageHasFocus:false,pointer:{x:100,y:120}}),evaluate:async()=>true,pressKey:async value=>calls.push({key:value})};
  await activateAndPress(session,'\uE007');
  assert.deepEqual(calls.map(c=>c.method||c.key),['browsingContext.activate','input.performActions','input.releaseActions','\uE007']);
  assert.equal(calls[1].params.actions[0].actions[0].origin,'viewport');
  session.evaluateJSON=async()=>({pageHasFocus:false,pointer:null});await assert.rejects(activateAndPress(session,'\uE007'),/no neutral/);
  session.evaluateJSON=async()=>({pageHasFocus:true});session.evaluate=async()=>false;await assert.rejects(activateAndPress(session,'\uE007'),/could not be restored/);
});
test('capture scroll compensates only page sticky headers, not top-layer dialogs',()=>{
  for(const modal of [true,false]) {
    const calls=[];const node={scrollIntoView:()=>calls.push('scroll'),closest:()=>modal?{}:null,getBoundingClientRect:()=>({top:0})};
    const context={document:{querySelector:selector=>selector==='.target'?node:{getBoundingClientRect:()=>({bottom:70})}},window:{scrollBy:args=>calls.push(args)}};
    new Script(scrollCaptureTargetExpression('.target')).runInNewContext(context);
    assert.equal(calls[0],'scroll');assert.equal(calls.length,modal?1:2);
    if(!modal)assert.equal(calls[1].top,-82);
  }
});
