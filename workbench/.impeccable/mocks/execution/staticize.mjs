// Render lanes.html's script to static HTML: both states, and one session panel per job, no JavaScript.
import fs from "node:fs";
const src = fs.readFileSync("lanes.html", "utf8");
const script = src.match(/<script>([\s\S]*?)<\/script>/)[1];
const app = { innerHTML: "" }, panel = { innerHTML: "", hidden: true };
globalThis.document = { getElementById: (id) => (id === "app" ? app : panel), addEventListener() {}, querySelectorAll: () => [] };
const run = new Function(script.replace(/\nrender\(\);\s*$/, "\n") + `
const out = {};
for (const st of ["now","busy"]) { state = st; render(); out[st] = { html: document.getElementById("app").innerHTML, panels: Object.keys(jobs).map(k => { open(k); return [k, document.getElementById("session").innerHTML]; }) }; }
return out;`);
const out = run();
const slug = (k, st) => `job-${st}-${k.replace(/[^a-z0-9]/gi, "-")}`;
const link = (html, st) => html
  .replace(/<button class="job ([^"]*)" data-job="([^"]+)"([^>]*)>/g, (_, c, k, rest) => `<a class="job ${c}" href="#${slug(k, st)}"${rest.replace(/ title="[^"]*"/, "")}>`)
  .replace(/<button class="jt" data-job="([^"]+)">/g, (_, k) => `<a class="jt" href="#${slug(k, st)}">`)
  .replace(/<\/button>/g, "</a>");
let body = "";
for (const st of ["now", "busy"]) {
  body += `<section class="state state-${st}">${link(out[st].html, st)}</section>`;
  for (const [k, p] of out[st].panels) body += `<aside class="session" id="${slug(k, st)}">${p.replace(/<button class="ghost" data-close>Close<\/button>/, '<a class="ghost" href="#">Close</a>').replace(/<form class="steer"[^>]*>([\s\S]*?)<\/form>/, '<div class="steer">$1</div>').replace(/<button class="primary">Send<\/button>/, '<span class="primary">Send</span>').replace(/<button class="ghost" type="button">Stop<\/button>/, '<span class="ghost">Stop</span>')}</aside>`;
}
let html = src
  .replace(/<script>[\s\S]*?<\/script>/, "")
  .replace(/<main id="app"><\/main>\s*<aside id="session"[^>]*><\/aside>/, `<main>${body}</main>`)
  .replace(/<div class="seg" role="tablist">[\s\S]*?<\/div><\/header>/, `<div class="seg"><input type="radio" name="st" id="st-now" checked><label for="st-now">Now <small>real, 11:20 UTC</small></label><input type="radio" name="st" id="st-busy"><label for="st-busy">Busy <small>example</small></label></div></header>`)
  .replace("</style>", `
/* no JavaScript: the radio pair picks a state; a job's link opens its panel by :target */
.seg input{position:absolute;opacity:0;pointer-events:none}
.seg label{color:#d6d6da;padding:5px 12px;border-radius:6px;cursor:pointer}.seg label small{color:var(--imute);margin-left:4px}
.seg input:checked+label{background:#fff;color:var(--ink)}.seg input:checked+label small{color:var(--mute)}
.seg input:focus-visible+label{outline:2px solid var(--amber);outline-offset:2px}
.state-busy{display:none}
body:has(#st-busy:checked) .state-busy{display:block}body:has(#st-busy:checked) .state-now{display:none}
a.job,a.jt{text-decoration:none;color:inherit;cursor:pointer}
a.job:hover{box-shadow:0 8px 20px -12px rgba(20,22,26,.4)}
.session{display:none}.session:target{display:flex}
a.ghost,span.ghost,span.primary{display:inline-flex;align-items:center;text-decoration:none;color:inherit}span.primary{color:#fff}
</style>`);
fs.writeFileSync("lanes.html", html);
console.log("static, bytes:", html.length, "scripts left:", (html.match(/<script/g) || []).length);
