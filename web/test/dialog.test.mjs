import { test } from "node:test";
import assert from "node:assert/strict";
import { createDialog } from "../assets/js/ui/dialog.js";

// Unit events call the helper directly. Native default Tab/Enter/Escape are
// separately exercised by onboarding-browser.mjs; no synthetic native claim.
class Element {
  constructor(tag) { this.tagName=tag.toUpperCase(); this.children=[]; this.attributes={}; this.listeners={}; this.visibility="visible"; this.rendered=true; }
  append(...nodes) { for (const node of nodes) { node.parentElement=this; this.children.push(node); } }
  setAttribute(key,value) { this.attributes[key]=String(value); }
  getAttribute(key) { return this.attributes[key] ?? null; }
  hasAttribute(key) { return key in this.attributes; }
  removeAttribute(key) { delete this.attributes[key]; }
  get tabIndex() { return this.hasAttribute("tabindex") ? Number(this.attributes.tabindex) : /^(BUTTON|INPUT|SELECT|TEXTAREA|SUMMARY|IFRAME|OBJECT)$/.test(this.tagName) || this.tagName==="A"&&this.hasAttribute("href") ? 0 : -1; }
  get isConnected() { return this===document.body || !!this.parentElement?.isConnected; }
  get isContentEditable() { return this.attributes.contenteditable==="true"; }
  get name() { return this.attributes.name || ""; }
  get form() { return this.closest("form"); }
  matches(selector) { return selector.split(",").some(part=>{
    const s=part.trim();
    if (s===":modal") return this.modal&&this.open;
    if (s===":disabled") return this.hasAttribute("disabled") || !!this.parentElement?.closest("fieldset[disabled]");
    const match=s.match(/^(\w+)?(?:\[([\w-]+)(?:="([^"]*)")?\])?$/);
    return !!match && (!match[1]||this.tagName===match[1].toUpperCase()) && (!match[2]||(this.hasAttribute(match[2])&&(match[3]===undefined||this.getAttribute(match[2])===match[3])));
  }); }
  closest(selector) { return this.matches(selector) ? this : this.parentElement?.closest(selector) || null; }
  querySelectorAll(selector) { return this.children.flatMap(node=>[...(node.matches(selector)?[node]:[]),...node.querySelectorAll(selector)]); }
  getClientRects() { return this.rendered && !this.closest("[hidden]") ? [{}] : []; }
  addEventListener(type,fn) { (this.listeners[type] ||= []).push(fn); }
  focus(options) { this.focusOptions=options; document.activeElement=this; }
  showModal() { this.open=true; this.modal=true; this.focus(); }
  close() { this.open=false; for (const fn of this.listeners.close||[]) fn(); }
  remove() { this.parentElement.children=this.parentElement.children.filter(node=>node!==this); this.parentElement=null; }
}
globalThis.HTMLElement=Element;
globalThis.document={body:new Element("body"),activeElement:null,createElement:tag=>new Element(tag)};
globalThis.getComputedStyle=node=>({visibility:node.visibility});
function setup(count=3,opts={}) {
  document.body.children=[]; document.activeElement=null;
  const opener=new Element("button"); document.body.append(opener); opener.focus();
  const modal=createDialog({titleId:"test-title",title:"Test",...opts});
  const buttons=Array.from({length:count},()=>new Element("button")); modal.body.append(...buttons); modal.open();
  return {...modal,buttons,opener};
}
function key(dialog,opts={}) {
  const event={key:"Tab",target:document.activeElement,defaultPrevented:false,preventDefault(){this.defaultPrevented=true;},...opts};
  for (const fn of dialog.listeners.keydown||[]) fn(event);
  return event;
}

test("dialog wraps only forward/reverse boundaries and preserves interior native Tab",()=>{
  const {dialog,buttons}=setup();
  buttons[2].focus(); assert.equal(key(dialog).defaultPrevented,true); assert.equal(document.activeElement,buttons[0]);
  assert.equal(buttons[0].focusOptions,undefined,"forward wrap permits native focus scrolling");
  assert.equal(key(dialog).defaultPrevented,false); assert.equal(document.activeElement,buttons[0]);
  assert.equal(key(dialog,{shiftKey:true}).defaultPrevented,true); assert.equal(document.activeElement,buttons[2]);
  assert.equal(buttons[2].focusOptions,undefined,"reverse wrap permits native focus scrolling");
  buttons[1].focus(); assert.equal(key(dialog).defaultPrevented,false); assert.equal(key(dialog,{shiftKey:true}).defaultPrevented,false);
});

test("dialog ignores disabled, negative roving tabs, hidden, unrendered, inert and visibility-hidden controls",()=>{
  const {dialog,body,buttons}=setup(2);
  const hiddenParent=new Element("div"); hiddenParent.setAttribute("hidden",""); hiddenParent.append(new Element("button"));
  const inertParent=new Element("div"); inertParent.setAttribute("inert",""); inertParent.append(new Element("input"));
  const fieldset=new Element("fieldset"); fieldset.setAttribute("disabled",""); fieldset.append(new Element("input"));
  const disabled=new Element("textarea"); disabled.setAttribute("disabled","");
  const roving=new Element("button"); roving.setAttribute("tabindex","-1");
  const invisible=new Element("input"); invisible.visibility="hidden";
  const collapsed=new Element("input"); collapsed.visibility="collapse";
  const notRendered=new Element("button"); notRendered.rendered=false;
  body.append(hiddenParent,inertParent,fieldset,disabled,roving,invisible,collapsed,notRendered);
  buttons[1].focus(); assert.equal(key(dialog).defaultPrevented,true); assert.equal(document.activeElement,buttons[0]);
});

test("each key recomputes reveal, copy fallback and platform roving state without stale candidates",()=>{
  const {dialog,body,buttons}=setup(2);
  const reveal=buttons[0],close=buttons[1],copy=new Element("button"),fallback=new Element("textarea"),platform=new Element("button");
  copy.setAttribute("disabled",""); fallback.setAttribute("hidden",""); platform.setAttribute("tabindex","-1");
  body.children=[]; body.append(platform,reveal,copy,fallback,close);
  close.focus(); key(dialog); assert.equal(document.activeElement,reveal);
  reveal.setAttribute("hidden",""); copy.removeAttribute("disabled");
  close.focus(); key(dialog); assert.equal(document.activeElement,copy);
  fallback.removeAttribute("hidden"); copy.setAttribute("disabled","");
  close.focus(); key(dialog); assert.equal(document.activeElement,fallback);
  platform.setAttribute("tabindex","0"); close.focus(); key(dialog); assert.equal(document.activeElement,platform);
  fallback.setAttribute("hidden",""); platform.focus(); key(dialog,{shiftKey:true}); assert.equal(document.activeElement,close);
});

test("positive tabindex and checked radio groups follow sequential boundary order",()=>{
  const {dialog,body,buttons}=setup(2);
  const positive=new Element("button"); positive.setAttribute("tabindex","2");
  const earlier=new Element("button"); earlier.setAttribute("tabindex","1");
  const radios=[new Element("input"),new Element("input")];
  for(const radio of radios){radio.setAttribute("type","radio");radio.setAttribute("name","choice");}
  radios[1].checked=true; body.append(positive,earlier,...radios);
  radios[1].focus(); key(dialog); assert.equal(document.activeElement,earlier);
  earlier.focus(); key(dialog,{shiftKey:true}); assert.equal(document.activeElement,radios[1]);
  radios[1].checked=false; radios[0].focus(); key(dialog); assert.equal(document.activeElement,earlier);
  buttons[0].focus(); assert.equal(key(dialog).defaultPrevented,false);
});

test("empty modal retains focus without forbidden dialog tabindex or invented controls",()=>{
  const {dialog,body}=setup(0);
  const hidden=new Element("button"); hidden.setAttribute("hidden",""); body.append(hidden);
  dialog.focus(); assert.equal(key(dialog).defaultPrevented,true); assert.equal(document.activeElement,dialog);
  assert.equal(key(dialog,{shiftKey:true}).defaultPrevented,true);
  assert.equal(dialog.hasAttribute("tabindex"),false); assert.equal(body.children.length,1);
});

test("nested/topmost modal and child-prevented events are not stolen by the parent",()=>{
  const outer=setup(1),inner=createDialog({titleId:"inner",title:"Inner"});
  const button=new Element("button"); inner.body.append(button); outer.body.append(inner.dialog); inner.open(); button.focus();
  assert.equal(key(outer.dialog).defaultPrevented,false); assert.equal(key(inner.dialog).defaultPrevented,true);
  assert.equal(document.activeElement,button);
  assert.equal(key(inner.dialog,{defaultPrevented:true}).defaultPrevented,true); assert.equal(document.activeElement,button);
  inner.close(); inner.dialog.remove(); outer.buttons[0].focus(); assert.equal(key(outer.dialog).defaultPrevented,true);
  outer.dialog.modal=false; assert.equal(key(outer.dialog).defaultPrevented,false);
  const sibling=setup(2); sibling.buttons[1].focus();
  key(sibling.dialog,{defaultPrevented:true}); assert.equal(document.activeElement,sibling.buttons[1],"child prevention must not wrap to the other boundary");
});

test("native editable content is a stop unless explicitly removed from sequential order",()=>{
  const {dialog,body,buttons}=setup(1);
  const editable=new Element("div"); editable.setAttribute("contenteditable","true");
  body.children=[]; body.append(editable,buttons[0]);
  buttons[0].focus(); key(dialog); assert.equal(document.activeElement,editable);
  editable.setAttribute("tabindex","-1"); buttons[0].focus(); key(dialog); assert.equal(document.activeElement,buttons[0]);
});

test("Tab handler leaves modified browser commands and native Escape/cancel semantics unchanged",()=>{
  const {dialog,buttons}=setup(1,{nonDismissable:true}); buttons[0].focus();
  for(const opts of [{ctrlKey:true},{altKey:true},{metaKey:true},{key:"F6"},{key:"l",ctrlKey:true},{key:"Escape"}]) assert.equal(key(dialog,opts).defaultPrevented,false);
  const cancel={defaultPrevented:false,preventDefault(){this.defaultPrevented=true;}};
  for(const fn of dialog.listeners.cancel)fn(cancel); assert.equal(cancel.defaultPrevented,true);
  assert.equal(dialog.open,true);
});

test("boundary handler does not change default or guarded explicit opener restoration",()=>{
  const modal=setup(1); modal.buttons[0].focus(); key(modal.dialog); modal.close(); assert.equal(document.activeElement,modal.opener);
  assert.equal(modal.opener.focusOptions.preventScroll,true,"closing still preserves the opener's reading position");
  modal.open({restoreFocusTo:modal.opener,shouldRestoreFocus:()=>false}); modal.buttons[0].focus(); modal.close(); assert.equal(document.activeElement,modal.buttons[0]);
});
