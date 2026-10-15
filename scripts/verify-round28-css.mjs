#!/usr/bin/env node
// Verify the one offline build retained every custom source class selector.
import { readFileSync, readdirSync } from 'node:fs';
import { join } from 'node:path';
const walk=dir=>readdirSync(dir,{withFileTypes:true}).flatMap(x=>x.isDirectory()?walk(join(dir,x.name)):[join(dir,x.name)]);
const classes=[...new Set(walk('web/src').filter(p=>p.endsWith('.css')).flatMap(p=>[...readFileSync(p,'utf8').matchAll(/\.((?:cp-|uplot)[a-zA-Z0-9_-]*)/g)].map(m=>m[1])))].sort();
const built=readFileSync('web/assets/app.css','utf8');
const missing=classes.filter(name=>!new RegExp(`\\.${name}(?![a-zA-Z0-9_-])`).test(built));
console.log(JSON.stringify({sourceClasses:classes.length,missing,tailwindVersion:JSON.parse(readFileSync('node_modules/@tailwindcss/cli/package.json','utf8')).version}));
if(missing.length)process.exitCode=1;
