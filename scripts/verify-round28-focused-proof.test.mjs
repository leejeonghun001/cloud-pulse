import test from 'node:test';
import assert from 'node:assert/strict';
import { Script } from 'node:vm';
import { readFileSync } from 'node:fs';
import { validateNativeWitness,validateFocusCycle,nativeTraceExpression,focusedInventoryHash } from './verify-round28-focused-proof.mjs';

const expected={key:'Tab',code:'Tab',keyCode:9,shift:false};
const event=type=>({type,phase:'after-handlers',key:'Tab',code:'Tab',keyCode:9,trusted:true,hasFocus:true,targetExpected:true,targetIsActive:true,defaultPrevented:true,shift:false});
const witness=()=>({before:{hasFocus:true,targetVisible:true,targetDisabled:false,targetFile:false},after:{hasFocus:true},events:[event('keydown'),event('keyup')],expected,outcome:true});
test('focused witness needs trusted expected native key, content focus and actual outcome',()=>{
  assert.equal(validateNativeWitness(witness()).pass,true);
  for(const mutate of [w=>w.before.hasFocus=false,w=>w.after.hasFocus=false,w=>w.events[0].hasFocus=false,w=>w.events[0].trusted=false,w=>w.events[0].targetExpected=false,w=>w.events[1].targetIsActive=false,w=>w.events[0].code='NumpadEnter',w=>w.outcome=false,w=>w.before.targetFile=true,w=>w.events.push(event('keydown'))]) {
    const w=witness();mutate(w);assert.equal(validateNativeWitness(w).pass,false);
  }
});
test('trusted post-chrome keydown is never proof of native activation',()=>{
  const w=witness();w.before.hasFocus=false;w.after.hasFocus=false;w.outcome=false;
  assert.equal(validateNativeWitness(w).pass,false);
  assert.ok(validateNativeWitness(w).errors.includes('expected native UI outcome absent'));
});
test('cycle demands 14 real contained steps and a prevented actual boundary wrap',()=>{
  const steps=Array.from({length:14},(_,i)=>({witness:{pass:true},before:{atBoundary:i===3,wrapTarget:7},after:{hasFocus:true,inDialog:true,targetInView:true,active:7},events:[event('keydown')]}));
  assert.equal(validateFocusCycle(steps,'forward').pass,true);
  assert.equal(validateFocusCycle(steps.slice(1),'forward').pass,false);
  for(const mutate of [s=>s[0].after.hasFocus=false,s=>s[3].after.active=8,s=>s[3].events[0].defaultPrevented=false,s=>s.forEach(x=>x.before.atBoundary=false),s=>s[0].witness.pass=false,s=>s[0].after.inDialog=false,s=>s[0].after.targetInView=false]) {
    const s=structuredClone(steps);mutate(s);assert.equal(validateFocusCycle(s,'backward').pass,false);
  }
});
test('key trace compiles, witnesses after handlers without dispatching synthetic events',()=>{
  assert.doesNotThrow(()=>new Script(nativeTraceExpression()));
  assert.match(nativeTraceExpression(),/e\.isTrusted/);assert.match(nativeTraceExpression(),/defaultPrevented/);assert.doesNotMatch(nativeTraceExpression(),/dispatchEvent|\.click\(/);
});
test('proof uses same-session raw input and never imports focus recovery',()=>{
  const source=readFileSync(new URL('./verify-round28-focused-proof.mjs',import.meta.url),'utf8');
  assert.doesNotMatch(source,/activateAndPress|browsingContext\.activate|\.click\(|dispatchEvent|new KeyboardEvent/);
  assert.match(source,/session\.pressKey/);assert.match(source,/14/);assert.match(source,/nextTarget/);
});
test('executor remains separate from full npm/Go/runtime matrices',()=>{
  const source=readFileSync(new URL('./verify-round28-focused-proof.sh',import.meta.url),'utf8');
  assert.match(source,/focusedProofGateAccepted/);assert.match(source,/--focused-proof/);assert.doesNotMatch(source,/npm test|go test|--firstimpression|sudo|systemctl/);
});
test('focused prebuild/source gates precede hub execution and full gate stays independent',()=>{
  const runner=readFileSync(new URL('./verify-phase1-browser.sh',import.meta.url),'utf8');
  assert.ok(runner.indexOf('validateFrozenProofSource')<runner.indexOf('reset-password --data-dir'));
  assert.match(runner,/if \(\( ! FOCUSED_PROOF \)\)/);
  const entry=readFileSync(new URL('./verify-round28-firstimpression.mjs',import.meta.url),'utf8');
  assert.match(entry,/plan\.focusedProofGateAccepted:plan\.runtimeGateAccepted/);
  assert.match(entry,/fullMatrixPerformed:false/);
});
test('source fingerprint uses canonical path/mode/sha256 order despite sorted manifest JSON members',()=>{
  const a={path:'source.js',mode:'100644',sha256:'a'.repeat(64)},b={mode:'100644',path:'source.js',sha256:'a'.repeat(64)};
  assert.equal(focusedInventoryHash([a]),focusedInventoryHash([b]));
  assert.notEqual(focusedInventoryHash([a]),focusedInventoryHash([{...a,sha256:'b'.repeat(64)}]));
});
