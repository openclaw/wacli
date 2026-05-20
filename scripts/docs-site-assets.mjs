export function css() {
  return `
:root{color-scheme:light;--ink:#0b1411;--text:#1f2a26;--muted:#5d6b66;--subtle:#8d9a95;--bg:#f6faf8;--paper:#ffffff;--accent:#128c7e;--accent-soft:rgba(18,140,126,.1);--accent-strong:#0e6b5f;--accent-on:#ffffff;--wa-green:#25d366;--wa-teal:#075e54;--wa-mint:#dcf8c6;--wa-blue:#34b7f1;--line:#e3ebe7;--line-soft:#eef4f1;--code-bg:#0c1f1a;--code-fg:#e6f3ed;--code-border:#14302a;--code-scroll:#1f4a40;--brand-mark-bg:#dcf8c6;--prompt:#62847b;--shadow-soft:0 4px 14px rgba(15,17,21,.08);--shadow-mobile:0 18px 40px rgba(15,17,21,.18)}
:root[data-theme="dark"]{color-scheme:dark;--ink:#ecf2ee;--text:#c8d3ce;--muted:#8a978f;--subtle:#5d6b66;--bg:#0a1411;--paper:#0f1a16;--accent:#25d366;--accent-soft:rgba(37,211,102,.14);--accent-strong:#3ee07a;--accent-on:#062018;--line:#1d2a25;--line-soft:#16201c;--code-bg:#04100c;--code-fg:#d6e5dd;--code-border:#1d3a32;--code-scroll:#27554a;--brand-mark-bg:rgba(37,211,102,.18);--prompt:#7ba398;--shadow-soft:0 4px 18px rgba(0,0,0,.45);--shadow-mobile:0 18px 40px rgba(0,0,0,.6)}
*{box-sizing:border-box}
html{scroll-behavior:smooth;scroll-padding-top:24px}
body{margin:0;background:var(--bg);color:var(--text);font-family:"Inter",ui-sans-serif,system-ui,-apple-system,Segoe UI,sans-serif;line-height:1.65;overflow-x:hidden;-webkit-font-smoothing:antialiased;font-feature-settings:"cv02","cv03","cv04","cv11";transition:background .2s,color .2s}
::selection{background:var(--accent);color:var(--accent-on)}
a{color:var(--accent);text-decoration:none;transition:color .12s}
a:hover{text-decoration:underline;text-underline-offset:.2em}
.shell{display:grid;grid-template-columns:268px minmax(0,1fr);min-height:100vh}
.sidebar{position:sticky;top:0;height:100vh;overflow:auto;padding:24px 22px;background:var(--paper);border-right:1px solid var(--line);scrollbar-width:thin;scrollbar-color:var(--line) transparent;transition:background .2s,border-color .2s}
.sidebar::-webkit-scrollbar{width:6px}
.sidebar::-webkit-scrollbar-thumb{background:var(--line);border-radius:6px}
.sidebar-head{display:flex;align-items:center;gap:10px;margin-bottom:24px}
.brand{display:flex;align-items:center;gap:11px;color:var(--ink);text-decoration:none;flex:1 1 auto;min-width:0}
.brand:hover{text-decoration:none}
.brand .mark{display:inline-flex;align-items:center;justify-content:center;width:32px;height:32px;border-radius:8px;background:var(--brand-mark-bg);flex:0 0 32px;transition:background .2s}
.brand .mark svg{display:block}
.brand strong{display:block;font-size:1.05rem;line-height:1.1;font-weight:600;letter-spacing:0;color:var(--ink)}
.brand small{display:block;color:var(--muted);font-size:.74rem;margin-top:3px;font-weight:400}
.theme-toggle{flex:0 0 auto;display:inline-flex;align-items:center;justify-content:center;width:36px;height:36px;border-radius:9px;background:transparent;border:1px solid var(--line);color:var(--muted);cursor:pointer;padding:0;transition:color .15s,border-color .15s,background .15s}
.theme-toggle:hover{color:var(--ink);border-color:var(--ink);background:var(--line-soft)}
.theme-toggle:focus-visible{outline:2px solid var(--accent);outline-offset:2px}
.theme-toggle svg{width:18px;height:18px;display:block}
.theme-toggle .icon-sun{display:none}
:root[data-theme="dark"] .theme-toggle .icon-sun{display:block}
:root[data-theme="dark"] .theme-toggle .icon-moon{display:none}
.search{display:block;margin:0 0 22px}
.search span{display:block;color:var(--muted);font-size:.7rem;font-weight:600;text-transform:uppercase;letter-spacing:0;margin-bottom:7px}
.search input{width:100%;border:1px solid var(--line);background:var(--paper);border-radius:8px;padding:9px 12px;font:inherit;font-size:.9rem;color:var(--text);outline:none;transition:border-color .15s,box-shadow .15s}
.search input:focus{border-color:var(--accent);box-shadow:0 0 0 3px var(--accent-soft)}
nav section{margin:0 0 18px}
nav h2{font-size:.68rem;color:var(--muted);text-transform:uppercase;letter-spacing:0;margin:0 0 6px;font-weight:600}
.nav-link{display:block;color:var(--text);text-decoration:none;border-radius:6px;padding:5px 10px;margin:1px 0;font-size:.9rem;line-height:1.4;transition:background .12s,color .12s}
.nav-link:hover{background:var(--line-soft);color:var(--ink);text-decoration:none}
.nav-link.active{background:var(--accent-soft);color:var(--accent);font-weight:600}
main{min-width:0;padding:32px clamp(20px,4.5vw,56px) 80px;max-width:1180px;margin:0 auto;width:100%}
.hero{display:flex;align-items:flex-end;justify-content:space-between;gap:22px;border-bottom:1px solid var(--line);padding:8px 0 22px;margin-bottom:8px;flex-wrap:wrap}
.hero-text{min-width:0;flex:1 1 320px}
.eyebrow{margin:0 0 8px;color:var(--muted);font-weight:600;text-transform:uppercase;letter-spacing:0;font-size:.7rem}
.hero h1{font-size:2.25rem;line-height:1.1;letter-spacing:0;margin:0;font-weight:700;color:var(--ink)}
.hero-meta{display:flex;gap:8px;flex:0 0 auto;flex-wrap:wrap}
.repo,.edit,.btn-ghost{border:1px solid var(--line);color:var(--text);text-decoration:none;border-radius:7px;padding:6px 11px;font-weight:500;font-size:.83rem;background:var(--paper);transition:border-color .15s,color .15s,background .15s}
.repo:hover,.edit:hover,.btn-ghost:hover{border-color:var(--ink);color:var(--ink);text-decoration:none}
.edit{color:var(--muted)}
.home-hero{padding:14px 0 28px;margin-bottom:8px;border-bottom:1px solid var(--line)}
.home-hero h1{font-size:3.25rem;line-height:1.04;letter-spacing:0;margin:0 0 .35em;font-weight:700;color:var(--ink)}
.home-hero .lede{font-size:1.18rem;line-height:1.55;color:var(--text);margin:0 0 1.2em;max-width:60ch}
.home-cta{display:flex;flex-wrap:wrap;gap:10px;align-items:center;margin:0 0 18px}
.home-cta .btn{display:inline-flex;align-items:center;gap:7px;min-height:46px;border-radius:8px;padding:0 16px;font-weight:600;font-size:.92rem;text-decoration:none;transition:background .15s,border-color .15s,color .15s,transform .12s}
.home-cta .btn-primary{background:var(--accent);color:var(--accent-on);border:1px solid var(--accent)}
.home-cta .btn-primary:hover{background:var(--accent-strong);border-color:var(--accent-strong);text-decoration:none;color:var(--accent-on)}
.home-cta .btn-ghost{padding:0 16px}
.home-install{display:flex;align-items:center;gap:14px;min-height:46px;background:var(--code-bg);color:var(--code-fg);border-radius:10px;padding:5px 10px 5px 18px;font:500 .95rem/1.2 "JetBrains Mono","SF Mono",ui-monospace,monospace;max-width:34em;border:1px solid var(--code-border)}
.home-install .prompt{color:var(--prompt);user-select:none;flex:0 0 auto}
.home-install code{flex:1 1 auto;background:transparent;border:0;color:var(--code-fg);font:inherit;padding:0;white-space:pre;overflow:auto}
.home-services{display:flex;flex-wrap:wrap;gap:6px;margin:6px 0 18px}
.home-services span{display:inline-block;padding:3px 9px;border:1px solid var(--line);border-radius:999px;font-size:.78rem;color:var(--muted);background:var(--paper)}
.doc-grid{display:grid;grid-template-columns:minmax(0,1fr);gap:48px;margin-top:24px}
.doc-grid-home{margin-top:8px}
@media(min-width:1180px){.doc-grid{grid-template-columns:minmax(0,72ch) 200px;justify-content:start}.doc-grid-home{grid-template-columns:minmax(0,76ch);justify-content:start}}
.doc{min-width:0;max-width:72ch;overflow-wrap:break-word}
.doc-home{max-width:76ch}
.doc h1{font-size:2.6rem;line-height:1.08;letter-spacing:0;margin:0 0 .4em;font-weight:700;color:var(--ink)}
body:not(.home) .doc>h1:first-child{display:none}
.doc h2{font-size:1.45rem;line-height:1.2;margin:2em 0 .5em;font-weight:600;letter-spacing:0;color:var(--ink);position:relative}
.doc h3{font-size:1.1rem;margin:1.7em 0 .35em;position:relative;font-weight:600;color:var(--ink);letter-spacing:0}
.doc h4{font-size:.98rem;margin:1.4em 0 .25em;color:var(--ink);position:relative;font-weight:600}
.doc h2:first-child,.doc h3:first-child,.doc h4:first-child{margin-top:.2em}
.doc :is(h2,h3,h4) .anchor{position:absolute;left:-1.05em;top:0;color:var(--subtle);opacity:0;text-decoration:none;font-weight:400;padding-right:.3em;transition:opacity .12s,color .12s}
.doc :is(h2,h3,h4):hover .anchor{opacity:.7}
.doc :is(h2,h3,h4) .anchor:hover{opacity:1;color:var(--accent);text-decoration:none}
.doc p{margin:0 0 1.05em}
.doc ul,.doc ol{padding-left:1.3rem;margin:0 0 1.15em}
.doc li{margin:.25em 0}
.doc li>p{margin:0 0 .4em}
.doc strong{font-weight:600;color:var(--ink)}
.doc em{font-style:italic}
.doc code{font-family:"JetBrains Mono","SF Mono",ui-monospace,monospace;font-size:.84em;background:var(--line-soft);border:1px solid var(--line);border-radius:5px;padding:.08em .35em;color:var(--ink)}
.doc pre{position:relative;overflow:auto;background:var(--code-bg);color:var(--code-fg);border-radius:8px;padding:18px 22px;margin:1.3em 0;font-size:.85em;line-height:1.6;scrollbar-width:thin;scrollbar-color:var(--code-scroll) transparent;border:1px solid var(--code-border)}
.doc pre::-webkit-scrollbar{height:8px;width:8px}
.doc pre::-webkit-scrollbar-thumb{background:var(--code-scroll);border-radius:8px}
.doc pre code{display:block;background:transparent;border:0;color:inherit;padding:0;font-size:1em;white-space:pre}
.doc pre .hl-c{color:#7f9990;font-style:italic}
.doc pre .hl-s{color:#a6e3a1}
.doc pre .hl-v{color:#f9c779}
.doc pre .hl-f{color:#89c2d9}
.doc pre .hl-n{color:#f4a47a}
.doc pre .hl-k{color:#cba6f7}
.copy{display:inline-flex;align-items:center;justify-content:center;flex:0 0 auto;width:34px;height:34px;background:rgba(255,255,255,.06);color:var(--code-fg);border:1px solid rgba(255,255,255,.18);border-radius:8px;padding:0;cursor:pointer;transition:background .15s,border-color .15s,color .15s,opacity .15s}
.copy:hover{background:rgba(255,255,255,.14);border-color:rgba(255,255,255,.3)}
.copy:focus-visible{outline:2px solid var(--accent);outline-offset:2px}
.copy svg{width:16px;height:16px;display:block;pointer-events:none}
.copy .icon-check{display:none}
.copy.copied{background:var(--wa-green);border-color:var(--wa-green);color:#062018}
.copy.copied .icon-clip{display:none}
.copy.copied .icon-check{display:block}
.home-install .copy{margin-left:auto}
.doc pre .copy{position:absolute;top:10px;right:10px;opacity:0}
.doc pre:hover .copy,.doc pre .copy:focus-visible,.doc pre .copy.copied{opacity:1}
.doc blockquote{margin:1.4em 0;padding:10px 16px;border-left:3px solid var(--accent);background:var(--accent-soft);border-radius:0 8px 8px 0;color:var(--text)}
.doc blockquote p:last-child{margin-bottom:0}
.doc table{width:100%;border-collapse:collapse;margin:1.2em 0;font-size:.92em}
.doc th,.doc td{border-bottom:1px solid var(--line);padding:9px 10px;text-align:left;vertical-align:top}
.doc th{font-weight:600;color:var(--ink);background:var(--line-soft);border-bottom:1px solid var(--line)}
.doc hr{border:0;border-top:1px solid var(--line);margin:2.2em 0}
.toc{position:sticky;top:24px;align-self:start;font-size:.84rem;padding-left:14px;border-left:1px solid var(--line);max-height:calc(100vh - 48px);overflow:auto;scrollbar-width:thin;scrollbar-color:var(--line) transparent}
.toc::-webkit-scrollbar{width:5px}
.toc::-webkit-scrollbar-thumb{background:var(--line);border-radius:5px}
.toc h2{font-size:.66rem;color:var(--muted);text-transform:uppercase;letter-spacing:0;margin:0 0 10px;font-weight:600}
.toc a{display:block;color:var(--muted);text-decoration:none;padding:4px 0 4px 10px;line-height:1.35;border-left:2px solid transparent;margin-left:-12px;transition:color .12s,border-color .12s}
.toc a:hover{color:var(--ink);text-decoration:none}
.toc a.active{color:var(--accent);border-left-color:var(--accent);font-weight:500}
.toc-l3{padding-left:22px!important;font-size:.94em}
@media(max-width:1179px){.toc{display:none}}
.page-nav{display:grid;grid-template-columns:1fr 1fr;gap:14px;margin-top:48px;border-top:1px solid var(--line);padding-top:20px}
.page-nav>a{display:block;border:1px solid var(--line);background:var(--paper);border-radius:9px;padding:13px 16px;text-decoration:none;color:var(--text);transition:border-color .15s,transform .15s,box-shadow .15s}
.page-nav>a:hover{border-color:var(--accent);text-decoration:none;color:var(--ink)}
.page-nav small{display:block;color:var(--muted);font-size:.7rem;text-transform:uppercase;letter-spacing:0;margin-bottom:5px;font-weight:600}
.page-nav span{display:block;font-weight:600;line-height:1.3;color:var(--ink)}
.page-nav-prev{text-align:left}
.page-nav-next{text-align:right;grid-column:2}
.page-nav-prev:only-child{grid-column:1}
.nav-toggle{display:none;position:fixed;top:14px;right:14px;top:calc(14px + env(safe-area-inset-top, 0px));right:calc(14px + env(safe-area-inset-right, 0px));z-index:20;width:40px;height:40px;border-radius:9px;background:var(--paper);border:1px solid var(--line);color:var(--ink);cursor:pointer;padding:10px 9px;flex-direction:column;align-items:stretch;justify-content:space-between;box-shadow:var(--shadow-soft)}
.nav-toggle span{display:block;width:100%;height:2px;flex:0 0 2px;background:currentColor;border-radius:2px;transition:transform .2s,opacity .2s}
.nav-toggle[aria-expanded="true"] span:nth-child(1){transform:translateY(8px) rotate(45deg)}
.nav-toggle[aria-expanded="true"] span:nth-child(2){opacity:0}
.nav-toggle[aria-expanded="true"] span:nth-child(3){transform:translateY(-8px) rotate(-45deg)}
@media(max-width:900px){
  .shell{display:block}
  .sidebar{position:fixed;inset:0 30% 0 0;max-width:320px;height:100vh;z-index:15;transform:translateX(-100%);transition:transform .25s ease,background .2s,border-color .2s;box-shadow:var(--shadow-mobile);background:var(--paper);pointer-events:none}
  .sidebar.open{transform:translateX(0);pointer-events:auto}
  .nav-toggle{display:flex}
  main{padding:64px 18px 56px}
  .hero{padding-top:6px}
  .hero h1{font-size:1.8rem}
  .home-hero h1{font-size:2.45rem}
  .doc h1{font-size:2.1rem}
  .hero-meta{width:100%;justify-content:flex-start}
  .home-hero{padding-top:8px}
  .doc{padding:0}
  .doc-grid{margin-top:18px;gap:24px}
  .doc :is(h2,h3,h4) .anchor{display:none}
}
@media(max-width:520px){
  main{padding:60px 14px 48px}
  .doc pre{margin-left:-14px;margin-right:-14px;border-radius:0;border-left:0;border-right:0}
  .home-install{flex-wrap:wrap;padding:14px 16px}
  .home-install .copy{margin-left:0}
}
`;
}

export function js() {
  return `
const COPY_ICON='${copyIconMarkup()}';
const html=document.documentElement;
const themeBtn=document.querySelector('.theme-toggle');
function applyTheme(t){html.setAttribute('data-theme',t);if(themeBtn){themeBtn.setAttribute('aria-pressed',t==='dark'?'true':'false');themeBtn.setAttribute('aria-label',t==='dark'?'Switch to light mode':'Switch to dark mode');themeBtn.title=t==='dark'?'Switch to light mode':'Switch to dark mode'}}
function readStoredTheme(){try{return localStorage.getItem('wacli-theme')}catch{return null}}
function storeTheme(t){try{localStorage.setItem('wacli-theme',t)}catch{}}
applyTheme(html.getAttribute('data-theme')||'light');
themeBtn?.addEventListener('click',()=>{const next=html.getAttribute('data-theme')==='dark'?'light':'dark';applyTheme(next);storeTheme(next)});
const prefersDark=window.matchMedia('(prefers-color-scheme: dark)');
const onPrefChange=()=>{if(readStoredTheme())return;applyTheme(prefersDark.matches?'dark':'light')};
if(prefersDark.addEventListener)prefersDark.addEventListener('change',onPrefChange);
else prefersDark.addListener?.(onPrefChange);
const sidebar=document.querySelector('.sidebar');
const navToggle=document.querySelector('.nav-toggle');
const mobileNav=window.matchMedia('(max-width: 900px)');
const sidebarFocusable='a[href],button,input,select,textarea,[tabindex]';
function setSidebarFocusable(enabled){
  sidebar?.querySelectorAll(sidebarFocusable).forEach((el)=>{
    if(enabled){
      if(el.dataset.sidebarTabindex!==undefined){
        if(el.dataset.sidebarTabindex)el.setAttribute('tabindex',el.dataset.sidebarTabindex);
        else el.removeAttribute('tabindex');
        delete el.dataset.sidebarTabindex;
      }
    }else if(el.dataset.sidebarTabindex===undefined){
      el.dataset.sidebarTabindex=el.getAttribute('tabindex')??'';
      el.setAttribute('tabindex','-1');
    }
  });
}
function setSidebarOpen(open){
  if(!sidebar||!navToggle)return;
  sidebar.classList.toggle('open',open);
  navToggle.setAttribute('aria-expanded',open?'true':'false');
  if(mobileNav.matches){
    sidebar.inert=!open;
    if(open)sidebar.removeAttribute('aria-hidden');
    else sidebar.setAttribute('aria-hidden','true');
    setSidebarFocusable(open);
  }else{
    sidebar.inert=false;
    sidebar.removeAttribute('aria-hidden');
    setSidebarFocusable(true);
  }
}
setSidebarOpen(false);
navToggle?.addEventListener('click',()=>setSidebarOpen(!sidebar?.classList.contains('open')));
document.addEventListener('click',(e)=>{if(!sidebar?.classList.contains('open'))return;if(sidebar.contains(e.target)||navToggle?.contains(e.target))return;setSidebarOpen(false)});
document.addEventListener('keydown',(e)=>{if(e.key==='Escape')setSidebarOpen(false)});
const syncSidebarForViewport=()=>setSidebarOpen(sidebar?.classList.contains('open')??false);
if(mobileNav.addEventListener)mobileNav.addEventListener('change',syncSidebarForViewport);
else mobileNav.addListener?.(syncSidebarForViewport);
const input=document.getElementById('doc-search');
input?.addEventListener('input',()=>{const q=input.value.trim().toLowerCase();document.querySelectorAll('nav section').forEach(sec=>{let any=false;sec.querySelectorAll('.nav-link').forEach(a=>{const m=!q||a.textContent.toLowerCase().includes(q);a.style.display=m?'block':'none';if(m)any=true});sec.style.display=any?'block':'none'})});
function attachCopy(target,getText,label){
  const btn=document.createElement('button');
  btn.type='button';
  btn.className='copy';
  btn.setAttribute('aria-label',label||'Copy to clipboard');
  btn.title=label||'Copy';
  btn.innerHTML=COPY_ICON;
  btn.addEventListener('click',async()=>{
    try{
      await navigator.clipboard.writeText(getText());
      btn.classList.add('copied');
      btn.setAttribute('aria-label','Copied');
      btn.title='Copied';
      setTimeout(()=>{btn.classList.remove('copied');btn.setAttribute('aria-label',label||'Copy to clipboard');btn.title=label||'Copy'},1400);
    }catch{
      btn.title='Copy failed';
      setTimeout(()=>{btn.title=label||'Copy'},1400);
    }
  });
  target.appendChild(btn);
}
document.querySelectorAll('.doc pre').forEach(pre=>attachCopy(pre,()=>pre.querySelector('code')?.textContent??'','Copy code'));
document.querySelectorAll('.home-install').forEach(el=>attachCopy(el,()=>el.querySelector('code')?.textContent??'','Copy install command'));
const tocLinks=document.querySelectorAll('.toc a');
if(tocLinks.length){const map=new Map();tocLinks.forEach(a=>{const id=a.getAttribute('href').slice(1);const el=document.getElementById(id);if(el)map.set(el,a)});const setActive=l=>{tocLinks.forEach(x=>x.classList.remove('active'));l.classList.add('active')};const obs=new IntersectionObserver(entries=>{const visible=entries.filter(e=>e.isIntersecting).sort((a,b)=>a.boundingClientRect.top-b.boundingClientRect.top);if(visible.length){const link=map.get(visible[0].target);if(link)setActive(link)}},{rootMargin:'-15% 0px -65% 0px',threshold:0});map.forEach((_,el)=>obs.observe(el))}
`;
}

export function themeBootstrapScript() {
  return `(function(){try{var s=localStorage.getItem('wacli-theme');var t=s||(window.matchMedia('(prefers-color-scheme: dark)').matches?'dark':'light');document.documentElement.setAttribute('data-theme',t);}catch(e){document.documentElement.setAttribute('data-theme','light');}})();`;
}

export function themeToggleMarkup() {
  return `<button class="theme-toggle" type="button" aria-label="Toggle dark mode" aria-pressed="false" title="Toggle dark mode">
        <svg class="icon-moon" viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M21 12.79A9 9 0 1 1 11.21 3 7 7 0 0 0 21 12.79z"/></svg>
        <svg class="icon-sun" viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.93 4.93l1.41 1.41M17.66 17.66l1.41 1.41M2 12h2M20 12h2M4.93 19.07l1.41-1.41M17.66 6.34l1.41-1.41"/></svg>
      </button>`;
}

function copyIconMarkup() {
  return '<svg class="icon-clip" viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><rect x="9" y="9" width="11" height="11" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg><svg class="icon-check" viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M5 12.5l4 4 10-10"/></svg>';
}

export function faviconSvg() {
  return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64" role="img" aria-label="wacli">
<rect width="64" height="64" rx="14" fill="#075e54"/>
<path d="M32 12a20 20 0 0 0-17.2 30.16L12 54l12.2-2.8A20 20 0 1 0 32 12Zm11.7 28.36c-.5 1.4-2.86 2.62-4 2.74-1 .12-2.3.16-3.74-.22a20.7 20.7 0 0 1-3.4-1.28c-5.96-2.58-9.84-8.62-10.14-9-.3-.4-2.4-3.18-2.4-6.06s1.5-4.28 2.04-4.86c.52-.58 1.14-.72 1.52-.72.38 0 .76 0 1.1.02.36.02.84-.14 1.3.98.5 1.22 1.66 4.1 1.8 4.4.16.3.26.66.04 1.06-.2.4-.32.66-.62 1-.32.36-.66.78-.94 1.04-.3.3-.62.62-.26 1.22.36.6 1.6 2.64 3.44 4.28 2.36 2.1 4.36 2.74 4.96 3.04.6.3.96.26 1.32-.16.34-.4 1.5-1.74 1.9-2.34.4-.6.8-.5 1.36-.3.56.2 3.5 1.66 4.1 1.96.6.3 1 .46 1.14.72.16.26.16 1.5-.32 2.86Z" fill="#25d366"/>
</svg>`;
}

export function socialCardSvg() {
  return `<svg xmlns="http://www.w3.org/2000/svg" width="1200" height="630" viewBox="0 0 1200 630" role="img" aria-labelledby="title desc">
  <title id="title">wacli documentation</title>
  <desc id="desc">WhatsApp in your terminal</desc>
  <defs>
    <linearGradient id="bg" x1="0" y1="0" x2="1" y2="1">
      <stop offset="0" stop-color="#06120f"/>
      <stop offset="0.52" stop-color="#0f2b22"/>
      <stop offset="1" stop-color="#102c3a"/>
    </linearGradient>
  </defs>
  <rect width="1200" height="630" fill="url(#bg)"/>
  <rect x="70" y="70" width="1060" height="490" rx="32" fill="#f7fbf8" fill-opacity="0.06" stroke="#d8f5df" stroke-opacity="0.18"/>
  <g transform="translate(114 112)">
    <rect width="128" height="128" rx="28" fill="#12231d"/>
    <path d="M64 22a42 42 0 0 0-36.08 63.38L22 106l21.38-5.63A42 42 0 1 0 64 22Zm24.6 59.55c-1.03 2.9-6.01 5.5-8.4 5.76-2.14.23-4.85.32-7.86-.48a43.5 43.5 0 0 1-7.12-2.67C52.68 78.72 44.53 66.03 43.88 65.23c-.57-.84-5-6.68-5-12.73 0-6.01 3.17-8.97 4.27-10.2 1.1-1.22 2.39-1.53 3.2-1.53.8 0 1.62 0 2.29.04.76.04 1.76-.29 2.73 2.06 1.03 2.56 3.47 8.59 3.76 9.26.32.65.52 1.39.1 2.23-.42.84-.65 1.35-1.3 2.1-.65.74-1.36 1.62-1.95 2.2-.65.65-1.34 1.3-.57 2.56.74 1.26 3.36 5.53 7.21 8.94 4.96 4.39 9.16 5.76 10.42 6.37 1.26.65 2 .52 2.71-.32.74-.88 3.13-3.65 3.97-4.9.84-1.26 1.68-1.03 2.86-.65 1.15.42 7.35 3.47 8.59 4.1 1.28.65 2.1.97 2.39 1.49.32.55.32 3.11-.71 6.01Z" fill="#25d366"/>
  </g>
  <text x="114" y="315" fill="#ffffff" font-family="Inter, -apple-system, BlinkMacSystemFont, Segoe UI, sans-serif" font-size="104" font-weight="700">wacli</text>
  <text x="114" y="388" fill="#dcfce7" font-family="Inter, -apple-system, BlinkMacSystemFont, Segoe UI, sans-serif" font-size="44" font-weight="600">WhatsApp in your terminal</text>
  <text x="114" y="468" fill="#b7c8c0" font-family="JetBrains Mono, ui-monospace, SFMono-Regular, Menlo, monospace" font-size="32">$ brew install openclaw/tap/wacli</text>
  <g fill="#9af1b7" font-family="Inter, -apple-system, BlinkMacSystemFont, Segoe UI, sans-serif" font-size="28" font-weight="600">
    <text x="742" y="150">sync</text>
    <text x="857" y="150">search</text>
    <text x="1002" y="150">send</text>
  </g>
</svg>`;
}
