#!/usr/bin/env node
// PREP by default. Runtime only after an owned fresh hub/final merge gate.
// Reuses BiDi; never starts a hub, merges catalogs, runs installers or writes docs.
import { createHash } from "node:crypto";
import { existsSync, mkdirSync, readFileSync, readlinkSync, statSync, writeFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { BidiSession, registerActiveSession } from "./verify-phase1-auth.mjs";
import { REPO_ROOT, ROUND28_ROOT, safeVerificationPath } from "./verify-round28-safety.mjs";
import { runRuntimeHooks, BILLING_FIXTURE, NOTIFY_FIXTURE, HEALTH_FIXTURE } from './verify-round28-runtime.mjs';
import { billingSaveStub, reducedAnimationsAreQuiet, activateAndPress, scrollCaptureTargetExpression } from './verify-round28-runtime-lib.mjs';
import { runFocusedNativeProof,validateFrozenProofSource } from './verify-round28-focused-proof.mjs';

const PLAN = join(ROUND28_ROOT, "capture-plan.json");
const MERGE_PLAN = join(ROUND28_ROOT, "locale-integration-plan.json");
const TEST_POST = /^\/api\/v1\/alerts\/channels\/(?:[1-9]\d*\/)?test$/;
const AUTH_POSTS = new Set(["/api/v1/auth/login", "/api/v1/auth/logout"]);
export function requestPolicy(rawURL, method = "GET") {
  const url = new URL(rawURL, "http://127.0.0.1:18190");
  if (!["http:", "ws:"].includes(url.protocol) || url.hostname !== "127.0.0.1" || url.port !== "18190" || url.username || url.password) return "blocked-outbound";
  method = method.toUpperCase();
  if (method === "POST" && TEST_POST.test(url.pathname)) return "mock-notification-test";
  if (!["GET", "HEAD"].includes(method) && !(method === "POST" && AUTH_POSTS.has(url.pathname))) return "blocked-write";
  return "loopback-read-or-auth";
}

export function flattenDelta(value, prefix = "", result = {}) {
  if (typeof value === "string") { if (!prefix) throw new Error("delta root must be an object"); if (Object.hasOwn(result, prefix)) throw new Error(`duplicate delta key: ${prefix}`); result[prefix] = value; return result; }
  if (!value || typeof value !== "object" || Array.isArray(value)) throw new Error(`catalog delta must have string leaves: ${prefix}`);
  for (const [key, child] of Object.entries(value)) {
    if (key.split(".").some((part) => ["__proto__", "constructor", "prototype"].includes(part))) throw new Error("unsafe catalog key");
    const path = prefix ? `${prefix}.${key}` : key;
    if (Object.hasOwn(result, path)) throw new Error(`duplicate delta key: ${path}`);
    flattenDelta(child, path, result);
  }
  return result;
}
const placeholders = (text) => [...new Set([...text.matchAll(/\{([a-zA-Z0-9_]+)\}/g)].map((match) => match[1]))].sort();
export function validateLocaleDeltas(entries) {
  const seen = new Map(), merged = { en: {}, ko: {} }, counts = [];
  for (const { lane, allowedNamespaces, delta } of entries) {
    const en = flattenDelta(delta.en), ko = flattenDelta(delta.ko);
    if (JSON.stringify(Object.keys(en).sort()) !== JSON.stringify(Object.keys(ko).sort())) throw new Error(`${lane}: EN/KO key mismatch`);
    for (const key of Object.keys(en)) {
      if (!allowedNamespaces.some((space) => key.startsWith(`${space}.`))) throw new Error(`${lane}: key outside agreed namespace: ${key}`);
      if (seen.has(key)) throw new Error(`namespace conflict ${key}: ${seen.get(key)} and ${lane}`);
      if (JSON.stringify(placeholders(en[key])) !== JSON.stringify(placeholders(ko[key]))) throw new Error(`${lane}: placeholder mismatch ${key}`);
      seen.set(key, lane); merged.en[key] = en[key]; merged.ko[key] = ko[key];
    }
    counts.push({ lane, keys: Object.keys(en).length });
  }
  return { counts, merged, totalKeys: seen.size };
}

export function validateCapturePlan(plan) {
  if (plan.hub !== "http://127.0.0.1:18190" || plan.publication.enabled !== false) throw new Error("capture plan must remain loopback and nonpublishing");
  if (!Array.isArray(plan.captures) || plan.captures.length < 5 || plan.captures.length > 8) throw new Error("plan requires 5-8 representative images");
  const ids = new Set();
  for (const shot of plan.captures) {
    if (!/^[a-z0-9-]+$/.test(shot.id) || ids.has(shot.id)) throw new Error("unsafe or duplicate capture ID"); ids.add(shot.id);
    if (!shot.route.startsWith("#/") || !["en", "ko"].includes(shot.lang) || !["dark", "light"].includes(shot.theme) || ![360, 768, 1440].includes(shot.width)) throw new Error(`invalid variant ${shot.id}`);
    if (!/^docs\/screenshots\/(?:ko\/)?[a-z0-9-]+\.png$/.test(shot.docTarget)) throw new Error("publication target must be an existing local screenshot filename");
    if (!existsSync(join(REPO_ROOT, shot.docTarget))) throw new Error(`existing screenshot target missing: ${shot.docTarget}`);
  }
  return { shots: ids.size, pendingSelectors: plan.captures.filter((shot) => shot.pendingSelectorReview).map((shot) => shot.id) };
}

export function installGuardExpression(origin, state) {
  return `(() => {
    const origin = ${JSON.stringify(origin)}, state = ${JSON.stringify(state)};
    const original = window.fetch.bind(window);
    const proof = window.__round28CaptureProof = {blocked:[],notificationTests:[],billingWrites:[],stubbedReads:[],realReads:[],mockOutcome:'accepted-not-delivery-verified'};
    const fixture = window.__round28Fixture = {state,hostMode:state === 'onboarding-runtime' ? 'loading' : state === 'empty-hosts' ? 'empty' : 'real',postMode:'accepted',pending:[],settled:0,billingSaveMode:'success',billingSaveAt:1780128001,billing:${JSON.stringify(BILLING_FIXTURE)}};
    const billingSaveStub = ${billingSaveStub.toString()};
    const json = (body,status=200) => new Response(JSON.stringify(body),{status,headers:{'Content-Type':'application/json'}});
    window.fetch = async (input, init = {}) => {
      if (window.__round28UploadObserved || [...document.querySelectorAll('input[type=file]')].some((node) => node.files?.length) ||
          (init.body instanceof FormData && [...init.body.values()].some((value) => value instanceof File && value.size > 0))) {
        window.__round28UploadObserved = true; throw new Error('STOP: original/source file upload observed');
      }
      const url = new URL(typeof input === 'string' ? input : input.url, location.href);
      const method = String(init.method || input?.method || 'GET').toUpperCase();
      if (url.origin !== origin) { proof.blocked.push({method,path:url.pathname,reason:'outbound'}); throw new Error('Round28 blocked outbound request'); }
      if (method === 'POST' && /^\\/api\\/v1\\/alerts\\/channels\\/(?:[1-9]\\d*\\/)?test$/.test(url.pathname)) {
        const body = typeof init.body === 'string' ? JSON.parse(init.body || '{}') : {};
        proof.notificationTests.push({method,path:url.pathname,keys:Object.keys(body),configKeys:Object.keys(body.config||{}),chatId:body.config?.chat_id||null,existingChannelId:body.existing_channel_id || null});
        if (fixture.postMode === 'deferred') return new Promise(resolve => fixture.pending.push((body,status=200) => {fixture.settled++;resolve(json(body,status));}));
        if (fixture.postMode === 'error') return json({ok:false,diagnosis:{code:'telegram_chat_not_found',detail:'Isolated fixture destination rejected',hint:'Check the destination'}},200);
        return json({ok:true,delivery:'accepted'});
      }
      if(state==='billing-fixture'&&method==='PUT'&&['/api/v1/settings/billing/currency','/api/v1/settings/billing/interval'].includes(url.pathname)) {
        const body=typeof init.body==='string'?JSON.parse(init.body):null;
        const result=billingSaveStub(fixture,method,url.pathname,body);
        proof.billingWrites.push({method,path:url.pathname,body,status:result.status,browserOnly:true});
        return json(result.body,result.status);
      }
      if (!['GET','HEAD'].includes(method) && !(['/api/v1/auth/login','/api/v1/auth/logout'].includes(url.pathname) && method === 'POST')) {
        proof.blocked.push({method,path:url.pathname,reason:'write'}); throw new Error('Round28 blocked non-auth write');
      }
      if (fixture.hostMode !== 'real' && url.pathname === '/api/v1/hosts') {
        proof.stubbedReads.push(url.pathname);
        if (fixture.hostMode === 'loading') {await new Promise(done=>setTimeout(done,1200));return json({error:'Isolated hosts-read unavailable'},503);}
        if (fixture.hostMode === 'error') return json({error:'Isolated hosts-read unavailable'},503);
        return json({hosts:[]});
      }
      if (['notification-accepted','notification-runtime','matrix'].includes(state) && url.pathname === '/api/v1/alerts/channels') {
        proof.stubbedReads.push(url.pathname); return json(${JSON.stringify(NOTIFY_FIXTURE)});
      }
      if (['notification-accepted','notification-runtime','matrix'].includes(state) && url.pathname === '/api/v1/alerts/channels/health') {
        proof.stubbedReads.push(url.pathname); return json(${JSON.stringify(HEALTH_FIXTURE)});
      }
      if (['billing-fixture','matrix'].includes(state) && url.pathname === '/api/v1/billing') {
        proof.stubbedReads.push(url.pathname); return json(fixture.billing);
      }
      if (['billing-fixture','matrix'].includes(state) && url.pathname === '/api/v1/settings/billing/currency') {
        proof.stubbedReads.push(url.pathname); return json(fixture.billing.display_currency);
      }
      if (['billing-fixture','matrix'].includes(state) && url.pathname === '/api/v1/audit') {
        proof.stubbedReads.push(url.pathname); return json({entries:[{id:9001,at:1780128000,actor:'Isolated documentation fixture',action:'update',entity_type:'billing_currency',entity_id:'fixture',before_json:'{"currency":"USD"}',after_json:'{"currency":"KRW","krw_per_usd":1385.5}'}],has_more:false});
      }
      proof.realReads.push({method,path:url.pathname,query:url.search,at:Date.now()});
      if (state.startsWith('threshold') && url.pathname === '/api/v1/hosts/demo-aws-web-01') {
        const response=await original(input,init),body=await response.json();
        body.latest={...body.latest,cpu_percent:Number(state.slice(9)),mem_used_percent:Number(state.slice(9)),disk_used_percent:Number(state.slice(9))};
        proof.stubbedReads.push(url.pathname);return json(body);
      }
      return original(input, init);
    };
    document.addEventListener('click',(event) => {
      const link=event.target.closest?.('a[href]');
      if (link && new URL(link.href,location.href).origin !== origin) { event.preventDefault(); proof.blocked.push({path:'external-anchor',reason:'outbound-navigation'}); }
    },true);
    const stopUpload = () => {
      if ([...document.querySelectorAll('input[type=file]')].some((node) => node.files?.length)) throw new Error('STOP: original/source file upload observed');
    };
    document.addEventListener('change',(event) => { if (event.target?.type === 'file' && event.target.files?.length) { window.__round28UploadObserved = true; event.preventDefault(); event.stopImmediatePropagation(); stopUpload(); } },true);
    return true;
  })()`;
}

async function waitFor(session, expression, label) {
  const deadline = Date.now() + 20_000;
  while (Date.now() < deadline) { try { if (await session.evaluate(expression)) return; } catch { /* Navigation realm transition. */ } await new Promise((done) => setTimeout(done, 100)); }
  throw new Error(`timed out: ${label}`);
}

async function installNetworkBackstop(session,report) {
  const intercept=await session.send('network.addIntercept',{phases:['beforeRequestSent']});
  session.websocket.addEventListener('message',event=>{
    const message=JSON.parse(event.data);
    if(message.method!=='network.beforeRequestSent'||!message.params.isBlocked)return;
    const request=message.params.request,policy=requestPolicy(request.url,request.method);
    const blocked=['blocked-outbound','blocked-write','mock-notification-test'].includes(policy);
    if(blocked)report.traffic.push({url:request.url,method:request.method,blocked:true});
    void session.send(blocked?'network.failRequest':'network.continueRequest',{request:request.request}).catch(error=>report.failures.push(String(error)));
  });
  await session.send('session.subscribe',{events:['network.beforeRequestSent']});
  return intercept;
}

async function captureReducedMotion({hub,password,out,report}) {
  const session=new BidiSession({reducedMotion:true});
  report.reducedMotion={coverage:'EN/light/360 masked guide only; 12-case normal-motion matrix is separate',checks:[],profileRemoved:false};
  const reduced=report.reducedMotion;
  const check=(name,pass,detail)=>{reduced.checks.push({name,pass:Boolean(pass),detail});if(!pass)report.failures.push(`reduced-motion: ${name}`);};
  registerActiveSession(session);
  try {
    await session.start();await installNetworkBackstop(session,report);
    const preload=await session.send('script.addPreloadScript',{functionDeclaration:`() => { ${installGuardExpression(hub,'empty-hosts')}; }`});
    await session.navigate(`${hub}/login`);await waitFor(session,"Boolean(document.getElementById('cp-login-password'))",'reduced-motion login');
    await session.evaluate(`document.getElementById('cp-login-username').value='admin';document.getElementById('cp-login-password').value=${JSON.stringify(password)};document.getElementById('cp-login-username').closest('form').requestSubmit();true`);
    await waitFor(session,"Boolean(localStorage.getItem('cp_session'))&&Boolean(document.getElementById('cp-onboarding-add-agent'))",'reduced-motion empty overview');
    await session.setViewport(360,800);
    await session.evaluate("localStorage.setItem('cp_lang','en');localStorage.setItem('cp_theme','light');true");
    await session.navigate(`${hub}/?round28_motion=reduce#/`);await waitFor(session,"document.documentElement.lang==='en'&&!document.documentElement.classList.contains('dark')&&Boolean(document.getElementById('cp-onboarding-add-agent'))",'reduced-motion variant');
    await session.evaluate("document.getElementById('cp-onboarding-add-agent').focus();true");await activateAndPress(session,'\uE007');
    await waitFor(session,"Boolean(document.querySelector('.cp-add-agent-dialog[open]'))",'reduced-motion masked guide');
    const proof=await session.evaluateJSON("({preference:matchMedia('(prefers-reduced-motion: reduce)').matches,upload:Boolean(window.__round28UploadObserved),masked:!document.querySelector('#cp-install-code code').textContent&&!document.querySelector('.cp-install-fallback').value,tokenInDOM:document.documentElement.outerHTML.includes('phase1-local-agent-token-0123456789'),animations:document.getAnimations().map(a=>({state:a.playState,duration:a.effect.getComputedTiming().duration,iterations:a.effect.getComputedTiming().iterations})),missing:[...document.querySelectorAll('[aria-labelledby]')].flatMap(n=>n.getAttribute('aria-labelledby').split(/\\s+/).filter(id=>!document.getElementById(id)?.textContent.trim())),resources:window.__verificationResourceErrors,csp:window.__verificationCSP})");
    if(proof.upload)throw new Error('STOP: original/source file upload observed');
    check('actual Firefox reduced-motion media preference enabled',proof.preference,proof);
    check('active motion absent or near-zero/single iteration',reducedAnimationsAreQuiet(proof.animations),proof.animations);
    check('masked guide, strict heading targets and clean assets/CSP',proof.masked&&!proof.tokenInDOM&&!proof.missing.length&&!proof.resources.length&&!proof.csp.length,proof);
    if(!proof.masked||proof.tokenInDOM)throw new Error('Refusing unmasked reduced-motion screenshot');
    const path=safeVerificationPath(join(out,'matrix/en-light-360-A-guide-reduced-motion.png'));await session.screenshot(path);reduced.screenshot=path;
    const cycle=[];
    for(let step=0;step<14;step++) {await session.pressTab();cycle.push(await session.evaluateJSON("({id:document.activeElement.id,tag:document.activeElement.tagName,pageHasFocus:document.hasFocus(),inside:Boolean(document.activeElement.closest('.cp-add-agent-dialog[open]'))})"));}
    check('14 native Tabs remain in reduced-motion modal and cycle focus',cycle.every(x=>x.inside)&&new Set(cycle.map(x=>x.tag+':'+x.id)).size>1&&cycle.slice(1).some(x=>x.tag===cycle[0].tag&&x.id===cycle[0].id),cycle);
    await activateAndPress(session,'\uE00C');await waitFor(session,"!document.querySelector('.cp-add-agent-dialog')",'reduced-motion Escape');
    check('Escape restores CTA focus',await session.evaluate("document.activeElement.id==='cp-onboarding-add-agent'"));
    await session.send('script.removePreloadScript',{script:preload.script});
    check('reduced-motion console/JS/assets clean',!session.consoleErrors.length&&!session.javascriptErrors.length&&!session.networkFailures.length,{console:session.consoleErrors,js:session.javascriptErrors,assets:session.networkFailures});
  } finally {await session.stop();registerActiveSession(null);reduced.profileRemoved=!session.profile||!existsSync(session.profile);}
}

async function capture({ hub, passwordFile, out, plan }) {
  const passwordPath = safeVerificationPath(passwordFile);
  if (statSync(passwordPath).mode & 0o077) throw new Error("password file must be private");
  const password = readFileSync(passwordPath, "utf8").replace(/\r?\n$/, "");
  safeVerificationPath(out, "capture output", ROUND28_ROOT); mkdirSync(out, { recursive: true, mode: 0o700 });
  const session = new BidiSession({reducedMotion:false}), report = { status: "running", screenshots: [], traffic: [], failures: [], publicationPerformed: false };
  registerActiveSession(session);
  try {
    await session.start();
    // BiDi-level cancellation covers subresources/WebSocket-like requests;
    // browser fetch stubs separately guarantee notification POSTs cannot send.
    const intercept = await installNetworkBackstop(session,report);
    await session.navigate(`${hub}/login`);
    await waitFor(session, "Boolean(document.getElementById('cp-login-password'))", "login");
    await session.evaluate(installGuardExpression(hub, "read-only"));
    await session.evaluate(`(() => { document.getElementById('cp-login-username').value='admin'; document.getElementById('cp-login-password').value=${JSON.stringify(password)}; document.getElementById('cp-login-username').closest('form').requestSubmit(); return true; })()`);
    await waitFor(session, "Boolean(localStorage.getItem('cp_session')) && location.hash === '#/'", "authenticated overview");
    await runRuntimeHooks(session, {hub, out, report, installGuardExpression});
    for (const shot of plan.captures) {
      if (shot.pendingSelectorReview) throw new Error(`capture selector still pending terminal lane review: ${shot.id}`);
      await session.setViewport(shot.width, shot.height);
      await session.evaluate(`localStorage.setItem('cp_lang',${JSON.stringify(shot.lang)});localStorage.setItem('cp_theme',${JSON.stringify(shot.theme)});true`);
      const preload = await session.send("script.addPreloadScript", { functionDeclaration: `() => { ${installGuardExpression(hub, shot.state)}; }` });
      await session.navigate(`${hub}/?round28_capture=${shot.id}${shot.route}`);
      await waitFor(session, `document.documentElement.lang===${JSON.stringify(shot.lang)} && Boolean(document.querySelector(${JSON.stringify(shot.readySelector)}))`, shot.id);
      if (shot.actionSelector) {
        await session.evaluate(`document.querySelector(${JSON.stringify(shot.actionSelector)})?.click();true`);
        await waitFor(session, `Boolean(document.querySelector(${JSON.stringify(shot.afterSelector)}))`, `${shot.id} action`);
      }
      if (shot.scrollSelector) await session.evaluate(scrollCaptureTargetExpression(shot.scrollSelector));
      await session.evaluate('new Promise(done=>requestAnimationFrame(()=>requestAnimationFrame(done)))');
      const proof = await session.evaluateJSON(`({pathname:location.pathname,hash:location.hash,lang:document.documentElement.lang,dark:document.documentElement.classList.contains('dark'),width:innerWidth,overflow:Math.max(document.documentElement.scrollWidth,document.body.scrollWidth)>innerWidth,traffic:window.__round28CaptureProof,resources:window.__verificationResourceErrors,csp:window.__verificationCSP,upload:Boolean(window.__round28UploadObserved)||[...document.querySelectorAll('input[type=file]')].some(node=>node.files?.length)})`);
      if (proof.upload) throw new Error("STOP: original/source file upload observed");
      if (proof.overflow || proof.csp?.length || proof.resources?.length || session.consoleErrors.length || session.javascriptErrors.length || session.networkFailures.length) throw new Error(`capture QA failure: ${shot.id}`);
      if (shot.state === "notification-accepted" && !proof.traffic?.notificationTests.length) throw new Error("notification capture did not exercise browser-only test stub");
      const privacy = await session.evaluateJSON(`({commandMasked:!document.querySelector('#cp-install-code code')?.textContent && !document.querySelector('.cp-install-fallback')?.value,tokenInDOM:document.documentElement.outerHTML.includes('phase1-local-agent-token-0123456789'),storedNotificationSecretInDOM:document.documentElement.outerHTML.includes('round28-private-fixture-not-real'),timelineVisible:(()=>{const r=document.querySelector('.cp-activity-event')?.getBoundingClientRect();return Boolean(r&&r.bottom>0&&r.top<innerHeight);})()})`);
      if (privacy.tokenInDOM || privacy.storedNotificationSecretInDOM || shot.state==='install-guide'&&!privacy.commandMasked || shot.id==='activity-en'&&!privacy.timelineVisible) throw new Error(`private capture gate failed: ${shot.id}`);
      const path = safeVerificationPath(join(out, `${shot.id}.png`));
      await session.screenshot(path, "viewport");
      report.screenshots.push({ id: shot.id, path, docTarget:shot.docTarget, sha256: createHash("sha256").update(readFileSync(path)).digest("hex"), fixture: shot.fixture, proof, privacy });
      await session.send("script.removePreloadScript", { script: preload.script });
    }
    await captureReducedMotion({hub,password,out,report});registerActiveSession(session);
    await session.send("network.removeIntercept", { intercept: intercept.intercept });
    report.status = report.failures.length ? 'runtime-failed-captures-private' : "captured-awaiting-manual-review";
  } catch (error) { report.failures.push(String(error.stack || error)); report.status = "failed"; }
  finally {
    await session.stop(); registerActiveSession(null);
    report.profileRemoved = !session.profile || !existsSync(session.profile);
    writeFileSync(safeVerificationPath(join(out, "report.json")), `${JSON.stringify(report, null, 2)}\n`, { mode: 0o600 });
  }
  return report;
}

async function focusedProof({hub,passwordFile,out}) {
  const passwordPath=safeVerificationPath(passwordFile);
  if(statSync(passwordPath).mode&0o077)throw new Error('Focused proof password must be private');
  const password=readFileSync(passwordPath,'utf8').replace(/\r?\n$/,'');
  safeVerificationPath(out,'focused proof output',ROUND28_ROOT);mkdirSync(out,{recursive:true,mode:0o700});
  const report={status:'running',focusedProfiles:[],traffic:[],failures:[],publicationPerformed:false,fullMatrixPerformed:false};
  for(const reducedMotion of [false,true]) {
    const session=new BidiSession({reducedMotion});registerActiveSession(session);
    let stopObserved=false;
    try {
      await session.start();await session.setViewport(1440,900);await installNetworkBackstop(session,report);
      const preload=await session.send('script.addPreloadScript',{functionDeclaration:`()=>{${installGuardExpression(hub,'empty-hosts')};}`});
      await session.navigate(`${hub}/login`);await waitFor(session,"Boolean(document.getElementById('cp-login-password'))",'focused login');
      await session.evaluate(`document.getElementById('cp-login-username').value='admin';document.getElementById('cp-login-password').value=${JSON.stringify(password)};document.getElementById('cp-login-username').closest('form').requestSubmit();true`);
      await waitFor(session,"Boolean(localStorage.getItem('cp_session'))&&Boolean(document.getElementById('cp-onboarding-add-agent'))",'focused empty overview');
      await session.evaluate(`localStorage.setItem('cp_lang','en');localStorage.setItem('cp_theme',${JSON.stringify(reducedMotion?'light':'dark')});true`);
      await session.navigate(`${hub}/?focused_motion=${reducedMotion?'reduce':'normal'}#/`);
      await waitFor(session,"document.documentElement.lang==='en'&&Boolean(document.getElementById('cp-onboarding-add-agent'))",'focused English overview');
      await session.send('script.removePreloadScript',{script:preload.script});
      const proof=await runFocusedNativeProof(session,{hub,guard:installGuardExpression,reducedMotion,report});
      proof.browser=session.capabilities;
    } catch(error){report.failures.push(String(error.stack||error));stopObserved=/STOP:/.test(error.message);}
    finally {
      await session.stop();registerActiveSession(null);
      const proof=report.focusedProfiles.find(p=>p.context===session.context);
      if(proof)proof.profileRemoved=!session.profile||!existsSync(session.profile);
      if(!proof)report.failures.push('Focused profile failed before native proof started');
    }
    if(stopObserved)break;
  }
  report.status=report.failures.length?'failed':'focused-proof-passed-awaiting-parent-full-QA';
  writeFileSync(safeVerificationPath(join(out,'report.json')),JSON.stringify(report,null,2)+'\n',{mode:0o600});
  return report;
}

async function main(argv) {
  const options = { plan: PLAN, mergePlan: MERGE_PLAN, out: join(ROUND28_ROOT, "capture-prep") };
  for (let i = 0; i < argv.length; i++) {
    const arg = argv[i];
    if (["--run", "--integration-accepted", "--focused-proof"].includes(arg)) { options[arg.slice(2)] = true; continue; }
    if (!["--plan", "--out", "--hub", "--password-file", "--owned-run-root", "--source-manifest"].includes(arg) || !argv[i + 1]) throw new Error("usage: --plan FILE --out DIR [--run --integration-accepted [--focused-proof --source-manifest FILE] --hub http://127.0.0.1:18190 --password-file FILE --owned-run-root DIR]");
    options[arg.slice(2)] = argv[++i];
  }
  const plan = JSON.parse(readFileSync(safeVerificationPath(options.plan), "utf8")), result = validateCapturePlan(plan);
  if (options.run) {
    if((options['focused-proof']?plan.focusedProofGateAccepted:plan.runtimeGateAccepted)!==true)throw new Error('Runtime gate closed: await parent source-terminal/build acceptance for selected proof/full phase');
    if (!options["integration-accepted"] || options.hub !== plan.hub || !options["password-file"]) throw new Error("runtime capture requires explicit parent integration acceptance and owned loopback hub credentials");
    if (result.pendingSelectors.length) throw new Error(`pending terminal selector acceptance: ${result.pendingSelectors.join(", ")}`);
    if (!options["owned-run-root"]) throw new Error("runtime capture requires the active runner's owned-run-root; never attach to an unknown listener");
    const ownedRoot = safeVerificationPath(options["owned-run-root"], "active runner root", ROUND28_ROOT);
    const pid = readFileSync(safeVerificationPath(join(ownedRoot, "owned-hub.pid")), "utf8").trim();
    if (!/^[1-9]\d*$/.test(pid)) throw new Error("invalid owned hub pid");
    const executable = readlinkSync(`/proc/${pid}/exe`);
    safeVerificationPath(executable, "owned hub executable", ROUND28_ROOT);
    const runningHash = createHash("sha256").update(readFileSync(`/proc/${pid}/exe`)).digest("hex");
    const pinnedHash = readFileSync(safeVerificationPath(join(ownedRoot, "running-binary-digest.txt")), "utf8").split(/\s+/)[0];
    if (runningHash !== pinnedHash) throw new Error("owned hub executable differs from the active runner's pinned runtime digest");
    if (resolve(options["password-file"]) !== join(ownedRoot, "credentials/admin.password")) throw new Error("password file must belong to the active owned runner");
    if(options['focused-proof']||options['source-manifest']) {
      if(!options['source-manifest'])throw new Error('Focused proof requires exact frozen source inventory');
      validateFrozenProofSource(options['source-manifest']);
    }
    const report = options['focused-proof']?await focusedProof({hub:options.hub,passwordFile:options['password-file'],out:options.out}):await capture({ hub: options.hub, passwordFile: options["password-file"], out: options.out, plan });
    if(options['focused-proof']||options['source-manifest']) {
      report.source=validateFrozenProofSource(options['source-manifest']);
      report.binarySha256=runningHash;
      writeFileSync(safeVerificationPath(join(options.out,'report.json')),JSON.stringify(report,null,2)+'\n',{mode:0o600});
    }
    if (report.failures.length) process.exitCode = 1;
    return;
  }
  const mergePlan = JSON.parse(readFileSync(MERGE_PLAN, "utf8"));
  const available = [], missing = [];
  for (const lane of mergePlan.deltas) {
    const path = safeVerificationPath(join(REPO_ROOT, lane.path));
    if (!existsSync(path)) missing.push(lane.lane);
    else available.push({ ...lane, delta: JSON.parse(readFileSync(path, "utf8")) });
  }
  const deltaProof = validateLocaleDeltas(available);
  safeVerificationPath(options.out, "prep output", ROUND28_ROOT); mkdirSync(options.out, { recursive: true, mode: 0o700 });
  const report = { status: "prepared-not-run", capturePlan: result, locales: { missingLanes: missing, counts: deltaProof.counts, totalKeys: deltaProof.totalKeys }, hubStarted: false, firefoxStarted: false, catalogsMerged: false, cssBuilt: false, screenshotsCaptured: false };
  writeFileSync(safeVerificationPath(join(options.out, "prep-report.json")), `${JSON.stringify(report, null, 2)}\n`, { mode: 0o600 });
  console.log(JSON.stringify(report, null, 2));
}
if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main(process.argv.slice(2)).catch((error) => { console.error(error.stack || error); process.exitCode = 1; });
}
