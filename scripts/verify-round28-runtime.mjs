// High-value terminal UI runtime hooks; browser stubs never reach dispatch.
import { mkdirSync, readFileSync } from 'node:fs';
import { join } from 'node:path';
import { REPO_ROOT, safeVerificationPath } from './verify-round28-safety.mjs';
import { formatUSD, formatDisplayAmount } from '../web/assets/js/core/currency.js';
import { setCatalogs, setLang } from '../web/assets/js/core/i18n.js';
import { compareRealBilling, activateAndPress, scrollCaptureTargetExpression } from './verify-round28-runtime-lib.mjs';

export const NOTIFY_FIXTURE = [{id:9001,name:'Isolated documentation fixture',type:'telegram',enabled:false,config:{bot_token:'round28-private-fixture-not-real',chat_id:'-1009001'}}];
const emptyStats={total:0,success:0,last_success_at:0,last_failure_at:0,avg_latency_ms:0,max_latency_ms:0};
export const HEALTH_FIXTURE = {banner_needed:false,channels:[{channel_id:9001,channel_name:'Isolated documentation fixture',channel_type:'telegram',enabled:false,state:'unknown',stats_7d:{...emptyStats},stats_30d:{...emptyStats},settings:{enabled:false,weekday:0,minute_of_day:0},recent_synthetic:[]}]};
// Current models.BillingView shape. No provider commands/refresh. Null fields
// are an explicitly separate robustness extension: Go float64 fields do not
// currently emit null, so that branch is not real-backend billing evidence.
export const BILLING_FIXTURE = {
  interval_seconds:43200,display_currency:{currency:'KRW',krw_per_usd:1385.5,rate_updated_at:1780128000},
  snapshots:[
    {provider:'aws',status:'auth_failed',status_detail:'Isolated fixture: collection unavailable',currency:'USD',mtd_cost:500.25,forecast_cost:900.5,forecast_method:'linear',last_success_at:1780128000,last_attempt_at:1780300800,account_level:true,stale:true},
    {provider:'oci',status:'not_configured',currency:'USD',mtd_cost:0,forecast_cost:0,last_success_at:0,last_attempt_at:0,account_level:false,stale:false}
  ],
  hosts:[
    {host_id:'demo-aws-web-01',hostname:'서울-프로덕션-아주긴호스트이름-network-secondary-02',provider:'aws',matched:false,cloud_mtd:500.25,cloud_forecast:900.5,network_estimate:{plan_id:3,mtd:1.23,projected:4.56},total_mtd:501.48,total_forecast:905.06},
    {host_id:'demo-oci-app-01',hostname:'isolated-null-zero-negative-robustness-fixture',provider:'oci',matched:true,cloud_mtd:null,cloud_forecast:0,network_estimate:{plan_id:0},total_mtd:null,total_forecast:-12.34}
  ]
};
const q = JSON.stringify;
const sleep = ms => new Promise(done=>setTimeout(done,ms));
async function wait(session, expression, label, timeout=20000) {
  const deadline=Date.now()+timeout;
  while(Date.now()<deadline) {try {if(await session.evaluate(expression)) return;} catch {} await sleep(80);}
  throw new Error(`timed out: ${label}`);
}
const geometry = `(() => {
  const missing=[...document.querySelectorAll('[aria-labelledby]')].flatMap(n=>n.getAttribute('aria-labelledby').split(/\\s+/).filter(id=>!document.getElementById(id)?.textContent.trim()));
  const ids=[...document.querySelectorAll('[id]')].map(n=>n.id),duplicates=ids.filter((id,i)=>ids.indexOf(id)!==i);
  return {width:innerWidth,documentWidth:Math.max(document.documentElement.scrollWidth,document.body.scrollWidth),missing,duplicates,
    title:document.querySelector('main h1')?.textContent.trim(),lang:document.documentElement.lang,dark:document.documentElement.classList.contains('dark'),
    resources:window.__verificationResourceErrors||[],csp:window.__verificationCSP||[],upload:Boolean(window.__round28UploadObserved)||[...document.querySelectorAll('input[type=file]')].some(n=>n.files?.length),
    secretInDOM:document.documentElement.outerHTML.includes('phase1-local-agent-token-0123456789')||document.documentElement.outerHTML.includes('round28-private-fixture-not-real')};
})()`;
export async function runRuntimeHooks(session,{hub,out,report,installGuardExpression}) {
  report.runtime={checks:[],matrix:[],stubs:'Browser GET JSON, delayed/error/accepted notification test POST, and narrowly matched currency/interval PUT stubs only; writes never reach hub. Health schedule/synthetic actions and billing refresh remain denied. Real isolated API reads for metrics/history/install guide. Null billing amounts are robustness-only, not Go float64 output.'};
  const runtime=report.runtime;
  const check=(name,pass,detail)=>{runtime.checks.push({name,pass:Boolean(pass),detail});if(!pass)report.failures.push(`runtime: ${name}`);};
  check('normal-motion primary Firefox preference is explicit',await session.evaluate("!matchMedia('(prefers-reduced-motion: reduce)').matches"));
  const catalogs=Object.fromEntries(['en','ko'].map(lang=>[lang,JSON.parse(readFileSync(join(REPO_ROOT,`web/assets/i18n/${lang}.json`),'utf8'))]));
  setCatalogs(catalogs);
  const matrixOut=safeVerificationPath(join(out,'matrix'));mkdirSync(matrixOut,{recursive:true,mode:0o700});
  let serial=0;
  const key=value=>activateAndPress(session,value);
  async function open(route,state,variant) {
    const {lang,theme,width,height}=variant;
    await session.setViewport(width,height);
    await session.evaluate(`localStorage.setItem('cp_lang',${q(lang)});localStorage.setItem('cp_theme',${q(theme)});localStorage.setItem('cp_overview_view','grid');true`);
    const preload=await session.send('script.addPreloadScript',{functionDeclaration:`() => { ${installGuardExpression(hub,state)}; }`});
    try {await session.navigate(`${hub}/?round28_runtime=${++serial}${route}`);}
    finally {await session.send('script.removePreloadScript',{script:preload.script});}
    await session.send('browsingContext.activate',{context:session.context});
    await wait(session,`document.documentElement.lang===${q(lang)} && document.documentElement.classList.contains('dark')===${theme==='dark'} && Boolean(document.querySelector('main h1'))`,'route locale/theme');
  }
  async function shot(id,variant,scrollSelector) {
    if(scrollSelector)await session.evaluate(scrollCaptureTargetExpression(scrollSelector));
    await session.evaluate('new Promise(done=>requestAnimationFrame(()=>requestAnimationFrame(done)))');
    const proof=await session.evaluateJSON(geometry);
    if(scrollSelector) {
      const scrolled=await session.evaluateJSON(`(()=>{const n=document.querySelector(${q(scrollSelector)}),r=n?.getBoundingClientRect();return {selector:${q(scrollSelector)},present:Boolean(n),visible:Boolean(r&&r.width>0&&r.height>0&&r.bottom>0&&r.top<innerHeight)};})()`);
      proof.scrolled=scrolled;
      check(`${id}: requested capture section is viewport-visible`,scrolled.present&&scrolled.visible,scrolled);
    }
    if(proof.upload)throw new Error('STOP: original/source upload observed');
    check(`${id}: geometry/heading targets/resources/privacy`,proof.documentWidth<=proof.width&&!proof.missing.length&&!proof.duplicates.length&&!proof.resources.length&&!proof.csp.length&&!proof.secretInDOM,proof);
    const path=safeVerificationPath(join(matrixOut,`${id}.png`));
    // Never capture an unmasked command or accidentally returned credential.
    if(!proof.secretInDOM)await session.screenshot(path);
    runtime.matrix.push({id,...variant,route:await session.evaluate('location.hash'),path,proof});
  }
  async function input(id,value) {
    await session.evaluate(`(()=>{const n=document.getElementById(${q(id)});if(!n)throw new Error('missing input ${id}');n.value=${q(value)};n.dispatchEvent(new Event(n.tagName==='SELECT'?'change':'input',{bubbles:true}));return true;})()`);
  }
  async function testClick() {await session.evaluate("document.querySelector('.cp-notify-test-head button').click();true");}
  async function settle(index,ok=true) {await session.evaluate(`window.__round28Fixture.pending[${index}](${q(ok?{ok:true,delivery:'accepted'}:{ok:false,diagnosis:{code:'telegram_chat_not_found',detail:'Isolated fixture rejected'}})});true`);await sleep(120);}
  async function onboarding(v,id) {
    const c=catalogs[v.lang];
    await open('#/','onboarding-runtime',v);
    await wait(session,"Boolean(document.querySelector('.cp-overview-load-state'))",'hosts loading');
    check(`${id}: loading has no confirmed empty/fake metrics`,await session.evaluate("!document.querySelector('.cp-onboarding')&&!document.querySelector('.cp-stat-card-value')&&!document.querySelector('.cp-systems-head .cp-page-subtitle')"));
    await shot(`${id}-loading`,v,'.cp-overview-load-state');
    await wait(session,"Boolean(document.querySelector('#cp-overview-retry'))",'hosts error');
    check(`${id}: error is not confirmed empty`,await session.evaluate("!document.querySelector('.cp-onboarding')&&!document.querySelector('.cp-stat-card-value')"));
    await shot(`${id}-error`,v,'.cp-overview-load-state');
    await session.evaluate("window.__round28Fixture.hostMode='empty';document.getElementById('cp-overview-retry').focus();true");
    await key('\uE007');
    await wait(session,"Boolean(document.getElementById('cp-onboarding-add-agent'))",'confirmed empty');
    check(`${id}: confirmed empty hides measured stat cards`,await session.evaluate("document.querySelector('.cp-stat-cards').hidden && !document.querySelector('.cp-stat-card-value') && document.querySelector('.cp-onboarding').textContent.includes("+q(c.overview.onboarding_title)+")"));
    await shot(`${id}-empty`,v);
    await shot(`${id}-empty-tail`,v,'.cp-onboarding-step:last-child');
    await session.evaluate("document.getElementById('cp-onboarding-add-agent').focus();true");await key('\uE007');
    await wait(session,"Boolean(document.querySelector('dialog[open].cp-add-agent-dialog'))",'CTA opens guide');
    const tabs=await session.evaluateJSON("[...document.querySelectorAll('.cp-add-agent-dialog [role=tab]')].map(n=>({id:n.id,label:n.textContent}))");
    check(`${id}: Linux/macOS/Windows actual guide tabs`,tabs.length===3&&tabs.some(x=>/Windows/.test(x.label))&&tabs.some(x=>/macOS/.test(x.label))&&tabs.some(x=>/Linux/.test(x.label)),tabs);
    check(`${id}: masked guide starts with zero command/token DOM`,await session.evaluate("!document.querySelector('#cp-install-code code').textContent && document.querySelector('#cp-install-code').hidden && !document.querySelector('.cp-install-fallback').value && document.querySelector('.cp-install-command-actions button:nth-child(2)').disabled"));
    await shot(`${id}-guide-masked`,v);
    for(const backward of [false,true]) {
      const focusCycle=[];
      for(let step=0;step<14;step++) {
        if(!backward)await session.pressTab();
        else {
          await session.send('input.performActions',{context:session.context,actions:[{type:'key',id:'keyboard',actions:[{type:'keyDown',value:'\uE008'},{type:'keyDown',value:'\uE004'},{type:'keyUp',value:'\uE004'},{type:'keyUp',value:'\uE008'}]}]});
          await session.send('input.releaseActions',{context:session.context});
        }
        focusCycle.push(await session.evaluateJSON("(()=>{const n=document.activeElement,d=document.querySelector('.cp-add-agent-dialog[open]'),r=n.getBoundingClientRect(),p=d.getBoundingClientRect();return {id:n.id,tag:n.tagName,pageHasFocus:document.hasFocus(),inDialog:d.contains(n),rect:{top:r.top,bottom:r.bottom,left:r.left,right:r.right},visible:r.width>0&&r.height>0&&r.top>=Math.max(0,p.top+d.clientTop)-1&&r.bottom<=Math.min(innerHeight,p.top+d.clientTop+d.clientHeight)+1&&r.left>=Math.max(0,p.left+d.clientLeft)-1&&r.right<=Math.min(innerWidth,p.left+d.clientLeft+d.clientWidth)+1,masked:!document.querySelector('#cp-install-code code').textContent&&!document.querySelector('.cp-install-fallback').value};})()"));
      }
      check(`${id}: 14 native ${backward?'ShiftTabs':'Tabs'} remain trapped, focused-visible and cycle masked guide focus`,focusCycle.every(x=>x.inDialog&&x.masked&&x.pageHasFocus&&x.visible)&&new Set(focusCycle.map(x=>`${x.tag}:${x.id}`)).size>1&&focusCycle.slice(1).some(x=>x.tag===focusCycle[0].tag&&x.id===focusCycle[0].id),focusCycle);
    }
    await shot(`${id}-guide-masked-tail`,v,'.cp-install-report-note');
    for(let index=0;index<tabs.length;index++) {
      await session.evaluate(`document.getElementById(${q(tabs[index].id)}).focus();true`);
      await key('\uE007');
      check(`${id}: tab ${index} terminal permissions localized`,await session.evaluate("["+[c.nav.install_terminal_linux,c.nav.install_terminal_macos,c.nav.install_terminal_windows].map(q).join(',')+"].includes(document.querySelector('.cp-install-terminal').textContent)"));
      await shot(`${id}-guide-platform-${index}-masked`,v,'.cp-install-panel');
      await session.evaluate("document.getElementById('cp-install-reveal').focus();true");await key('\uE007');
      check(`${id}: explicit reveal is manual-only`,await session.evaluate("document.querySelector('#cp-install-code code').textContent.includes('phase1-local-agent-token-0123456789') && !document.querySelector('#cp-install-code').hidden && document.activeElement===document.querySelector('.cp-install-command-actions button:nth-child(2)')"));
      // A failure stub prevents actual clipboard/system writes while covering
      // the real manual fallback path, without exporting command bytes.
      await session.evaluate("Object.defineProperty(navigator,'clipboard',{configurable:true,value:{writeText:async()=>{throw new Error('Round28 isolated clipboard failure')}}});document.querySelector('.cp-install-command-actions button:nth-child(2)').click();true");
      await wait(session,"!document.querySelector('.cp-install-fallback').hidden",'manual copy fallback');
      check(`${id}: clipboard fallback is selected, read-only`,await session.evaluate("(()=>{const n=document.querySelector('.cp-install-fallback');return n.readOnly && document.activeElement===n && n.selectionEnd===n.value.length && n.value.includes('phase1-local-agent-token-0123456789');})()"));
      await session.evaluate(`document.getElementById(${q(tabs[index].id)}).focus();true`);await key('\uE014');
      check(`${id}: keyboard tab change remasks/cleans fallback`,await session.evaluate("!document.querySelector('#cp-install-code code').textContent && !document.querySelector('.cp-install-fallback').value && document.querySelector('.cp-install-fallback').hidden"));
    }
    await key('\uE00C');await wait(session,"!document.querySelector('dialog.cp-add-agent-dialog')",'Escape cleanup');
    const escaped=await session.evaluateJSON("({focusID:document.activeElement.id,focusTag:document.activeElement.tagName,tokenInDOM:document.documentElement.outerHTML.includes('phase1-local-agent-token-0123456789')})");
    check(`${id}: Escape restores connected CTA focus and removes token`,escaped.focusID==='cp-onboarding-add-agent'&&!escaped.tokenInDOM,escaped);
  }
  async function notifications(v,id) {
    const c=catalogs[v.lang].settings.notifications;
    await open('#/settings/notifications','notification-runtime',v);await wait(session,"Boolean(document.querySelector('.cp-channel-row'))",'notification fixture');
    const health=await session.evaluate("document.querySelector('.cp-notify-health-summary').textContent");
    const checklist=await session.evaluate("JSON.stringify(Object.keys(localStorage).filter(k=>k.includes('checklist')).map(k=>[k,localStorage.getItem(k)]))");
    await session.evaluate("document.querySelector('.cp-channel-row-actions button:nth-child(2)').focus();true");await key('\uE007');
    await wait(session,"Boolean(document.querySelector('.cp-notify-dialog[open]'))",'edit dialog');
    check(`${id}: stored secret excluded from DOM/value/attributes`,await session.evaluate("document.querySelector('#cp-channel-field-bot_token').value==='' && !document.documentElement.outerHTML.includes('round28-private-fixture-not-real') && !document.querySelector('#cp-channel-field-bot_token').hasAttribute('value') && !document.querySelector('#cp-channel-type')"));
    await input('cp-channel-name','');await session.evaluate("document.getElementById('cp-channel-name').focus();true");await key('\uE007');
    check(`${id}: native Enter blank name blocked with focus`,await session.evaluate("document.getElementById('cp-channel-name').getAttribute('aria-invalid')==='true' && document.activeElement.id==='cp-channel-name' && window.__round28CaptureProof.notificationTests.length===0 && window.__round28CaptureProof.blocked.length===0"));
    await input('cp-channel-name','Edited isolated fixture');await input('cp-channel-field-chat_id','-1009002');await testClick();
    await wait(session,"Boolean(document.querySelector('.cp-channel-dialog-test-result .cp-notify-test-accepted'))",'accepted draft');
    const post=await session.evaluateJSON('window.__round28CaptureProof.notificationTests.at(-1)');
    check(`${id}: edited draft ID+changed chat, omitted stored bot token`,post.path==='/api/v1/alerts/channels/test'&&post.existingChannelId===9001&&post.chatId==='-1009002'&&!post.configKeys.includes('bot_token'),post);
    check(`${id}: accepted ≠ delivered, health/checklist unchanged`,await session.evaluate("document.querySelector('.cp-notify-test-accepted').textContent.includes("+q(c.test_accepted_telegram)+") && document.querySelector('.cp-notify-test-accepted').textContent.includes("+q(c.test_health_unchanged)+") && document.querySelector('.cp-notify-health-summary').textContent==="+q(health)+" && JSON.stringify(Object.keys(localStorage).filter(k=>k.includes('checklist')).map(k=>[k,localStorage.getItem(k)]))==="+q(checklist)));
    await shot(`${id}-draft-accepted`,v,'.cp-channel-dialog-test-result');
    await input('cp-channel-field-chat_id','-1009003');
    check(`${id}: relevant edit clears result`,await session.evaluate("!document.querySelector('.cp-channel-dialog-test-result .cp-notify-test-accepted')"));
    await session.evaluate("window.__round28Fixture.postMode='deferred';true");await testClick();
    await wait(session,"window.__round28Fixture.pending.length===1",'deferred old draft');
    check(`${id}: sending locks save, duplicate test blocked`,await session.evaluate("(()=>{const b=document.querySelector('.cp-notify-test-head button');b.click();return b.disabled&&document.querySelector('.cp-notify-form button[type=submit]').disabled&&window.__round28Fixture.pending.length===1;})()"));
    await shot(`${id}-draft-pending-context`,v,'.cp-notify-context');
    await shot(`${id}-draft-pending-result`,v,'.cp-notify-test-panel');
    await input('cp-channel-name','Current draft');
    check(`${id}: pending name change clears/re-enables`,await session.evaluate("!document.querySelector('.cp-notify-test-accepted')&&!document.querySelector('.cp-notify-test-head button').disabled"));
    await session.evaluate("window.__round28Fixture.postMode='error';true");await testClick();
    await wait(session,"Boolean(document.querySelector('.cp-notify-test-failed'))",'new failed draft');await settle(0);
    check(`${id}: late old accepted result discarded after newer error`,await session.evaluate("Boolean(document.querySelector('.cp-notify-test-failed'))&&!document.querySelector('.cp-channel-dialog-test-result .cp-notify-test-accepted')"));
    await shot(`${id}-draft-failure-context`,v,'.cp-notify-context');
    await shot(`${id}-draft-failure-result`,v,'.cp-notify-test-failed');
    await input('cp-channel-field-chat_id','-1009004');await session.evaluate("window.__round28Fixture.postMode='deferred';true");await testClick();await wait(session,"window.__round28Fixture.pending.length===2",'cancel pending');
    await key('\uE00C');await wait(session,"!document.querySelector('.cp-notify-dialog')",'cancel removes dialog');await settle(1);
    check(`${id}: cancel discards late success and restores edit focus`,await session.evaluate("!document.querySelector('.cp-notify-dialog')&&!document.querySelector('.cp-notify-test-accepted')&&document.activeElement===document.querySelector('.cp-channel-row-actions button:nth-child(2)')"));
    await session.evaluate("document.querySelector('.cp-notify-header button').click();true");await wait(session,"Boolean(document.getElementById('cp-channel-type'))",'add dialog');
    await input('cp-channel-type','telegram');await input('cp-channel-field-bot_token','123456789:round28-dummy-not-a-real-token');await input('cp-channel-field-chat_id','-1009005');await testClick();await wait(session,"window.__round28Fixture.pending.length===3",'add type pending');
    await input('cp-channel-type','webhook');await settle(2);
    check(`${id}: add type change clears config/result and late response`,await session.evaluate("!document.querySelector('#cp-channel-field-bot_token') && !document.querySelector('.cp-notify-test-accepted') && document.querySelector('#cp-channel-field-url').value===''"));
    await key('\uE00C');await wait(session,"!document.querySelector('.cp-notify-dialog')",'add cleanup');
    await session.evaluate("window.__round28Fixture.postMode='accepted';document.querySelector('.cp-channel-row-actions button:first-child').click();true");await wait(session,"Boolean(document.querySelector('.cp-channel-row-status .cp-notify-test-accepted'))",'saved test');
    const saved=await session.evaluateJSON('window.__round28CaptureProof.notificationTests.at(-1)');
    check(`${id}: saved row uses saved endpoint, no request body`,saved.path==='/api/v1/alerts/channels/9001/test'&&saved.keys.length===0,saved);
    await shot(`${id}-saved-accepted`,v,'.cp-channel-row-status');
    await session.evaluate("document.getElementById('cp-notify-tab-channels').focus();true");await key('\uE014');
    await wait(session,"document.getElementById('cp-notify-tab-health')?.getAttribute('aria-selected')==='true'&&Boolean(document.querySelector('.cp-channel-health-grid'))",'native Health tab');
    check(`${id}: Health metadata independent, disabled schedule, no synthetic write`,await session.evaluate("document.querySelector('#cp-notify-panel').getAttribute('aria-labelledby')==='cp-notify-tab-health' && document.querySelector('#cp-notify-panel').textContent.includes("+q(c.health_evidence_note)+") && document.querySelector('#cp-notify-panel').textContent.includes("+q(c.health_no_synthetic)+") && !document.querySelector('#cp-notify-panel').textContent.includes('NaN') && !document.querySelector('.cp-channel-health-schedule input[type=checkbox]').checked && window.__round28CaptureProof.blocked.length===0"));
    await shot(`${id}-health-tab`,v,'.cp-notify-tabs');
    await shot(`${id}-health-details`,v,'.cp-channel-health-schedule');
  }
  async function billing(v,id) {
    setLang(v.lang);const c=catalogs[v.lang];
    await open('#/costs','billing-fixture',v);await wait(session,"document.querySelector('.cp-costs-content')?.getAttribute('aria-busy')==='false'&&Boolean(document.querySelector('.cp-costs-table'))",'billing fixture');
    const values=await session.evaluateJSON("[...document.querySelector('.cp-costs-table tbody tr').querySelectorAll('dd')].map(n=>n.textContent)");
    const h=BILLING_FIXTURE.hosts[0],expected=[h.cloud_mtd,h.cloud_forecast,h.network_estimate.mtd,h.network_estimate.projected,h.total_mtd,h.total_forecast].map(n=>formatDisplayAmount(n,BILLING_FIXTURE.display_currency,{taxNote:false}));
    check(`${id}: exact provided API amount strings preserved (not recomputed)`,JSON.stringify(values)===JSON.stringify(expected),{values,expected});
    const provider=await session.evaluateJSON("[...document.querySelector('.cp-costs-provider-card').querySelectorAll('.cp-costs-provider-value')].map(n=>n.textContent)");
    check(`${id}: provider native currency actual/forecast separate`,JSON.stringify(provider)===JSON.stringify([500.25,900.5].map(n=>formatUSD(n,'USD',{taxNote:false}))),provider);
    check(`${id}: stale/account scope/tax/unavailable distinct from zero`,await session.evaluate("(()=>{const text=document.querySelector('.cp-costs-page').textContent;const row=document.querySelectorAll('.cp-costs-table tbody tr')[1];return ["+[c.costs.account_total_only,c.costs.account_projection_note,c.costs.stale_data,c.costs.last_known_note,c.costs.all_amounts_excl_tax,c.costs.amounts_unavailable].map(q).join(',')+"].every(s=>text.includes(s)) && row.querySelector('.cp-costs-amount').textContent==="+q(c.costs.unavailable)+" && [...row.querySelectorAll('.cp-costs-amount')].some(n=>n.textContent.includes('0')) && [...row.querySelectorAll('.cp-costs-amount')].some(n=>n.textContent.includes('-'));})()"));
    await shot(`${id}-costs-values`,v);
    const stale=await session.evaluateJSON("[...document.querySelectorAll('.cp-costs-table tbody tr:first-child p.cp-costs-source-note')].map(n=>({tag:n.tagName,text:n.textContent,marginTop:parseFloat(getComputedStyle(n).marginTop),gap:n.getBoundingClientRect().top-n.previousElementSibling.getBoundingClientRect().bottom}))");
    check(`${id}: stale/last-known warnings are separate paragraphs with at least 4px spacing`,stale.length===3&&stale.every(n=>n.tag==='P'&&n.marginTop>=4&&n.gap>=3.5)&&stale.filter(n=>n.text===c.costs.stale_data).length===2,stale);
    for(const [label,selector] of [['cloud','.cp-costs-table tbody tr:first-child td:nth-child(2)'],['network','.cp-costs-table tbody tr:first-child td:nth-child(3)'],['total','.cp-costs-table tbody tr:first-child td:last-child'],['robustness-cloud','.cp-costs-table tbody tr:last-child td:nth-child(2)'],['robustness-total','.cp-costs-table tbody tr:last-child td:last-child']])await shot(`${id}-costs-${label}-scrolled`,v,selector);
    await open('#/settings/billing','billing-fixture',v);await wait(session,"Boolean(document.getElementById('cp-billing-save-currency'))",'billing controls');
    check(`${id}: tax/native controls current exact values`,await session.evaluate("document.querySelector('.cp-billing-tax-note').textContent==="+q(c.billing.tax_note)+" && document.getElementById('cp-billing-interval').value==='12h' && document.getElementById('cp-billing-krw-rate').value==='1385.5' && document.getElementById('cp-billing-save-currency').type==='submit'"));
    await input('cp-billing-interval','6h');
    check(`${id}: local interval hint follows selection without provider calls`,await session.evaluate("document.getElementById('cp-billing-interval-hint').textContent==="+q(c.billing.interval_6h)+" && window.__round28CaptureProof.blocked.length===0"));
    await input('cp-billing-krw-rate','0');await session.evaluate("document.getElementById('cp-billing-krw-rate').focus();true");await key('\uE007');
    check(`${id}: native Enter invalid rate focuses field without write`,await session.evaluate("document.getElementById('cp-billing-krw-rate').getAttribute('aria-invalid')==='true' && document.activeElement.id==='cp-billing-krw-rate' && window.__round28CaptureProof.blocked.length===0"));
    await shot(`${id}-billing-native-validation`,v);
    await input('cp-billing-krw-rate','1400.25');await session.evaluate("document.getElementById('cp-billing-krw-rate').focus();true");await key('\uE007');
    await wait(session,"document.getElementById('cp-billing-currency-status')?.textContent==="+q(c.billing.saved), 'browser-only currency save success');
    const currencyWrite=await session.evaluateJSON('window.__round28CaptureProof.billingWrites.at(-1)');
    check(`${id}: currency PUT fixture exact payload and durable feedback/focus`,currencyWrite.method==='PUT'&&currencyWrite.path==='/api/v1/settings/billing/currency'&&JSON.stringify(currencyWrite.body)===JSON.stringify({currency:'KRW',krw_per_usd:1400.25})&&await session.evaluate("document.activeElement.id==='cp-billing-save-currency'&&document.getElementById('cp-billing-krw-rate').value==='1400.25'&&window.__round28CaptureProof.blocked.length===0"),currencyWrite);
    await shot(`${id}-billing-save-success`,v,'.cp-billing-controls');
    await input('cp-billing-interval','6h');await session.evaluate("document.getElementById('cp-billing-save-interval').focus();true");await key('\uE007');
    await wait(session,"document.getElementById('cp-billing-interval-status')?.textContent==="+q(c.billing.saved_takes_effect),'browser-only interval save');
    check(`${id}: interval PUT fixture preserves native selection and focus`,await session.evaluate("document.getElementById('cp-billing-interval').value==='6h'&&document.activeElement.id==='cp-billing-save-interval'&&window.__round28CaptureProof.billingWrites.at(-1).body.interval==='6h'&&window.__round28CaptureProof.blocked.length===0"));
    await session.evaluate("window.__round28Fixture.billingSaveMode='server-error';true");await input('cp-billing-krw-rate','1500.5');await session.evaluate("document.getElementById('cp-billing-krw-rate').focus();true");await key('\uE007');
    const expectedSaveError=c.billing.save_failed.replace('{reason}',c.errors.internal_error);
    await wait(session,"document.getElementById('cp-billing-currency-status')?.textContent==="+q(expectedSaveError),'browser-only save server error');
    check(`${id}: failed PUT keeps prior fixture settings and user draft, re-enables save`,await session.evaluate("window.__round28Fixture.billing.display_currency.krw_per_usd===1400.25&&document.getElementById('cp-billing-krw-rate').value==='1500.5'&&!document.getElementById('cp-billing-save-currency').disabled&&window.__round28CaptureProof.billingWrites.at(-1).status===500&&window.__round28CaptureProof.blocked.length===0"));
    await shot(`${id}-billing-save-server-error`,v,'.cp-billing-controls');
    await wait(session,"Boolean(document.querySelector('.cp-billing-history .cp-table-wrap'))",'audit fixture');
    await session.evaluate("document.querySelector('.cp-billing-history').scrollIntoView({block:'start'});document.querySelector('.cp-billing-history .cp-table-wrap').focus();true");
    const scroll=await session.evaluateJSON("(()=>{const n=document.querySelector('.cp-billing-history .cp-table-wrap');return {focus:document.activeElement===n,range:n.scrollWidth-n.clientWidth,before:n.scrollLeft,label:document.getElementById(n.getAttribute('aria-labelledby'))?.textContent};})()");
    if(scroll.range>0) {await key('\uE014');await sleep(250);}
    check(`${id}: audit contained keyboard scroll, visible heading`,scroll.focus&&Boolean(scroll.label)&&(scroll.range<=0||await session.evaluate("document.querySelector('.cp-billing-history .cp-table-wrap').scrollLeft>"+scroll.before)),scroll);
    await shot(`${id}-billing-audit-scroll`,v);
  }
  const routes=[['overview','#/','read-only',"Boolean(document.querySelector('.cp-host-grid a'))"],['detail','#/host/demo-aws-web-01','read-only',"Boolean(document.querySelector('.cp-detail-chart-summary'))"],['activity','#/alerts','read-only',"document.querySelector('.cp-activity-card')?.getAttribute('aria-busy')==='false'&&Boolean(document.querySelector('.cp-activity-event'))"],['notifications','#/settings/notifications','matrix',"Boolean(document.querySelector('.cp-channel-row'))"],['costs','#/costs','matrix',"Boolean(document.querySelector('.cp-costs-table'))"],['settings-billing','#/settings/billing','matrix',"Boolean(document.getElementById('cp-billing-save-currency'))"]];
  for(const lang of ['en','ko'])for(const theme of ['dark','light'])for(const width of [360,768,1440]) {
    const v={lang,theme,width,height:width===768?1024:width===360?800:900},id=`${lang}-${theme}-${width}`;
    for(const [name,route,state,ready] of routes) {
      try {await open(route,state,v);await wait(session,ready,`${name} ready`);await shot(`${id}-${name}`,v,name==='activity'?'.cp-activity-card':null);}
      catch(error){if(/STOP:/.test(error.message))throw error;check(`${id}-${name}: route runtime`,false,String(error));}
    }
    for(const [name,fn] of [['A',onboarding],['B',notifications],['D',billing]]) {
      try {await fn(v,`${id}-${name}`);}
      catch(error){if(/STOP:/.test(error.message))throw error;check(`${id}-${name}: interaction completion`,false,String(error));}
    }
  }
  const v={lang:'en',theme:'dark',width:360,height:800};
  try {
    setLang('en');await open('#/costs','read-only',v);await wait(session,"document.querySelector('.cp-costs-content')?.getAttribute('aria-busy')==='false'&&Boolean(document.querySelector('.cp-costs-hosts'))",'real isolated billing');
    const real=await session.evaluateJSON("(async()=>{const response=await fetch('/api/v1/billing',{headers:{Authorization:'Bearer '+localStorage.getItem('cp_session')}});return {status:response.status,body:await response.json()};})()");
    const ui=await session.evaluateJSON("({rows:[...document.querySelectorAll('.cp-costs-table tbody tr')].map(n=>({href:n.querySelector('a').getAttribute('href'),values:[...n.querySelectorAll('.cp-costs-total-cell dd')].map(v=>v.textContent)})),emptyVisible:[...document.querySelectorAll('.cp-costs-hosts > p.cp-muted')].some(n=>n.textContent==="+q(catalogs.en.costs.no_hosts_reporting_yet)+")&&!document.querySelector('.cp-costs-table')})");
    const comparison=compareRealBilling(real,ui);
    check('real isolated billing API totals (billing off) render unchanged',comparison.pass,{billingEnabled:false,...comparison});
  } catch(error){if(/STOP:/.test(error.message))throw error;check('real isolated billing comparison',false,String(error));}
  try {
    for(const [value,level] of [[64.9,'ok'],[65,'warning'],[89.9,'warning'],[90,'critical']]) {
      await open('#/host/demo-aws-web-01',`threshold${value}`,v);await wait(session,"Boolean(document.querySelector('[data-detail-resource=cpu] .cp-bar-fill'))",'threshold');
      const result=await session.evaluateJSON("[...document.querySelectorAll('[data-detail-resource] .cp-bar-fill')].map(n=>({class:n.className,width:n.style.width}))");
      check(`threshold ${value}: actual browser bars use ${level}`,result.length===3&&result.every(x=>x.class.includes(`cp-bar-${level}`)),result);
    }
    await open('#/host/demo-aws-web-01','read-only',v);await wait(session,"document.querySelector('.cp-charts-grid')?.getAttribute('aria-busy')==='false'&&Boolean(document.querySelector('.cp-detail-chart-summary'))",'initial range');
    const initial=await session.evaluate("document.querySelector('#cp-detail-loaded-range').textContent");
    await session.evaluate(`(() => {
      const original=fetch.bind(window);window.__r28Metrics=[];
      window.fetch=async(input,init={})=>{
        const url=new URL(typeof input==='string'?input:input.url,location.href);
        if(url.pathname.endsWith('/metrics')) {
          const index=window.__r28Metrics.length,record={range:url.searchParams.get('range'),settled:false};window.__r28Metrics.push(record);
          const response=await original(input,{...init,signal:undefined}),body=await response.json();body.cpu=body.cpu.map(()=>[11,22,33][index]??44);
          return new Promise(done=>record.resolve=()=>{record.settled=true;done(Response.json(body));});
        }
        return original(input,init);
      };return true;
    })()`);
    await input('cp-host-range','1h');await wait(session,"Boolean(window.__r28Metrics[0]?.resolve)",'old A fetched');
    await input('cp-host-range','6h');await wait(session,"Boolean(window.__r28Metrics[1]?.resolve)",'B fetched');
    check('range retained while B loading discloses loaded A',await session.evaluate("document.querySelector('#cp-detail-loaded-range').textContent==="+q(initial)+" && document.querySelector('.cp-charts-grid').getAttribute('aria-busy')==='true' && document.querySelector('.cp-detail-history-notice').textContent.includes("+q(catalogs.en.host.range_6h)+") && document.querySelector('.cp-detail-chart-summary')!==null"));
    await input('cp-host-range','1h');await wait(session,"Boolean(window.__r28Metrics[2]?.resolve)",'new A fetched');
    await session.evaluate('window.__r28Metrics[2].resolve();true');await wait(session,"document.querySelector('.cp-detail-chart-summary')?.textContent.includes('33.0')",'new A33 loaded');
    await session.evaluate('window.__r28Metrics[1].resolve();window.__r28Metrics[0].resolve();true');await sleep(250);
    check('A-B-A generations discard late old A/B without loaded-range corruption',await session.evaluate("document.querySelector('.cp-detail-chart-summary').textContent.includes('33.0') && document.querySelector('#cp-detail-loaded-range').textContent==="+q(initial)+" && document.querySelector('.cp-charts-grid').getAttribute('aria-busy')==='false'"));
    await shot('regression-range-ABA',v,'.cp-detail-history');
  } catch(error){if(/STOP:/.test(error.message))throw error;check('range/threshold regression completion',false,String(error));}
  try {
    await open('#/alerts','read-only',v);await wait(session,"document.querySelectorAll('.cp-activity-event').length===50",'50 real history rows');
    await session.evaluate("document.getElementById('cp-activity-load-more').focus();true");await key('\uE007');await wait(session,"document.querySelectorAll('.cp-activity-event').length===55",'page2 real history');
    const ids=await session.evaluateJSON("[...document.querySelectorAll('.cp-activity-event')].map(n=>n.dataset.eventId)");
    await session.evaluate("document.querySelector('.cp-activity-card select').focus();window.scrollTo(0,1100);true");
    const before=await session.evaluateJSON("({scroll:scrollY,focus:document.activeElement.id,count:window.__round28CaptureProof.realReads.filter(x=>x.path==='/api/v1/alerts/active').length,historyReads:window.__round28CaptureProof.realReads.filter(x=>x.path==='/api/v1/alerts/events').length})");
    await wait(session,"window.__round28CaptureProof.realReads.filter(x=>x.path==='/api/v1/alerts/active').length>"+before.count+" && document.querySelector('.cp-activity-card').getAttribute('aria-busy')==='false'",'actual scheduled refresh',26000);
    const after=await session.evaluateJSON("({scroll:scrollY,focus:document.activeElement.id,ids:[...document.querySelectorAll('.cp-activity-event')].map(n=>n.dataset.eventId),count:window.__round28CaptureProof.realReads.filter(x=>x.path==='/api/v1/alerts/active').length,historyReads:window.__round28CaptureProof.realReads.filter(x=>x.path==='/api/v1/alerts/events').length})");
    check('loaded Activity page2 retained across real scheduled refresh + native focus/scroll',ids.length===55&&JSON.stringify(ids)===JSON.stringify(after.ids)&&before.focus===after.focus&&Math.abs(before.scroll-after.scroll)<=2&&after.count>before.count&&after.historyReads===before.historyReads,{before,after});
    const overflow=await session.evaluateJSON("(()=>{const n=document.querySelector('.cp-activity-audit-wrap,.cp-activity-table-wrap');if(!n)return {present:false};n.focus();return {present:true,focus:document.activeElement===n,range:n.scrollWidth-n.clientWidth,scroll:n.scrollLeft,role:n.getAttribute('role'),label:n.getAttribute('aria-label')};})()");
    if(overflow.present&&overflow.range>0) {await key('\uE014');await sleep(250);check('Activity focused audit scroll region responds to native ArrowRight',await session.evaluate("document.activeElement.scrollLeft>"+overflow.scroll),overflow);}
    await shot('regression-activity-loaded-history',v,'.cp-activity-card');
  } catch(error){if(/STOP:/.test(error.message))throw error;check('Activity scheduled/history regression completion',false,String(error));}
  runtime.consoleErrors=session.consoleErrors;runtime.javascriptErrors=session.javascriptErrors;runtime.networkFailures=session.networkFailures;
  check('all feature routes: console/JS/assets clean',!session.consoleErrors.length&&!session.javascriptErrors.length&&!session.networkFailures.length,{console:session.consoleErrors,js:session.javascriptErrors,assets:session.networkFailures});
}
