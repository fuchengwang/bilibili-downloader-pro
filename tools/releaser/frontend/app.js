const $ = id => document.getElementById(id);
const escapeHTML = value => String(value ?? '').replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
const labels = {pending:'等待',running:'进行中',complete:'已完成',failed:'需处理',skipped:'未启用',stopped:'已停止',interrupted:'已中断'};
const idleSteps = [
  {id:'preflight',name:'发布前检查',message:'确认项目、提交和账号连接'},
  {id:'build',name:'双平台构建 · 签名公证',message:'GitHub 构建 Windows ZIP 和签名公证的 macOS DMG'},
  {id:'assets',name:'下载安装包 · 校验',message:'核对正式构建文件的大小和 SHA-256'},
  {id:'gitcode',name:'GitCode 发布 · 下载校验',message:'同步代码和标签，上传并验证安装包'},
  {id:'lanzou',name:'蓝奏云上传 · 匿名校验',message:'验证两个新版下载后，清理旧版 BBDown 安装包'},
  {id:'updater',name:'云端登记 · 试下载 · 发布',message:'自动配置更新链接，试下载通过后上线'}
];
let state,selectedKey='',polling=false,action=false,lastBusy=false,forceNew=false;
const api = (method,...args) => window.go.main.App[method](...args);
function toast(message){$('toast').textContent=String(message);$('toast').classList.remove('hidden');clearTimeout(toast.timer);toast.timer=setTimeout(()=>$('toast').classList.add('hidden'),6500);}
function resumable(s){return !!s.job&&!forceNew;}
function render(s){
  state=s; const j=resumable(s)?s.job:null, busy=s.busy, version=j?.version||s.version||'—';
  $('version').textContent=version==='—'?version:'v'+version;
  $('commit').textContent=(j?.commit||s.commit)?.slice(0,10)+' · '+(j?'任务已固定提交':'当前代码提交');
  $('projectButton').textContent=s.settings.project?.split('/').at(-1)||'选择项目目录';
  const key=(j?'job:':'new:')+version;if(key!==selectedKey){$('notes').value=j?.notes||'';selectedKey=key;}
  $('notes').disabled=busy||!!j;
  const finished=j?.status==='complete'&&!(s.settings.cloudEnabled&&!j.cloudEnabled);
  $('startButton').disabled=busy||action||finished;
  $('startButton').textContent=busy?(s.activity==='release'?'正在发布…':'正在登录…'):finished?'本次发布已完成':j?(j.status==='complete'?'补做云端更新':'继续发布 v'+version):'开始发布 v'+version;
  $('stopButton').classList.toggle('hidden',!busy||s.activity==='cloud-login');$('stopButton').textContent=s.activity==='release'?'停止本机任务':'取消登录';
  $('startHint').textContent=j?'继续时复用已完成的步骤，并校验本次固定的版本。':'开始后会推送版本标签并发布安装包。';
  $('liveBadge').textContent=busy?(s.activity==='release'?'● 正在发布':'● 正在登录'):j?labels[j.status]||'准备继续':'准备发布';$('liveBadge').classList.toggle('active',busy);
  $('headline').textContent=j?j.status==='complete'?'发布已完成':busy?'发布正在进行':j.status==='failed'?'处理后，继续发布':'从上次进度继续':'每一步，都看得见';
  $('subline').textContent=j?'v'+j.version+' · '+j.commit.slice(0,10)+' · '+(j.cloudEnabled?'包含云端更新联动':'GitCode + 蓝奏云'):'先检查，再构建；上传后校验，最后发布云端更新。';
  const steps=j?.steps||idleSteps.map(v=>({...v,status:v.id==='updater'&&!s.settings.cloudEnabled?'skipped':'pending'}));
  const complete=steps.filter(v=>v.status==='complete'||v.status==='skipped').length;
  $('progress').style.width=(j?complete/steps.length*100:0)+'%';
  $('steps').innerHTML=steps.map((step,i)=>`<article class="step ${escapeHTML(step.status)}"><div class="step-number">${step.status==='complete'?'✓':step.status==='failed'?'!':String(i+1).padStart(2,'0')}</div><div class="step-body"><div class="step-title"><strong>${escapeHTML(step.name)}</strong><span class="step-state">${labels[step.status]||'等待'}</span></div><p>${escapeHTML(step.message||'等待前序步骤完成')}</p></div>${step.id==='preflight'?'<button class="open-step" data-settings="true">查看设置</button>':`<button class="open-step" data-open="${step.id}">打开 ↗</button>`}</article>`).join('');
  const checks=s.checks||[];
  $('checks').innerHTML=checks.map(c=>`<div class="check ${c.ok?'ok':'needs'}"><span>${c.ok?'✓':'○'}</span><div title="${escapeHTML(c.message)}">${escapeHTML(c.name)}</div>${!c.ok&&['github','gitcode-login','lanzou-login','cloud'].includes(c.id)?`<button class="text-button" ${c.id==='cloud'?'data-settings="true"':`data-login="${c.id.replace('-login','')}"`}>登录</button>`:''}</div>`).join('');
  $('checkProblems').innerHTML=checks.filter(c=>!c.ok).map(c=>`<div class="check needs"><span>○</span><div title="${escapeHTML(c.message)}">${escapeHTML(c.name)}</div>${['github','gitcode-login','lanzou-login','cloud'].includes(c.id)?`<button class="text-button" ${c.id==='cloud'?'data-settings="true"':`data-login="${c.id.replace('-login','')}"`}>登录</button>`:''}</div>`).join('');
  $('checkSummary').textContent=checks.filter(c=>c.ok).length+' 项已就绪 · 查看全部';
  const history=s.history||[];const historyHTML='<option value="">当前版本 · 新任务</option>'+history.map(h=>`<option value="${escapeHTML(h.version)}">v${escapeHTML(h.version)} · ${labels[h.status]||h.status}</option>`).join('');
  if($('history').innerHTML!==historyHTML)$('history').innerHTML=historyHTML;$('history').value=j?.version||'';$('history').disabled=busy;
  $('newJobButton').classList.toggle('hidden',!j||j.version===s.version&&j.status!=='complete');
  $('elapsed').textContent=j?elapsed(j):'—';
  $('jobError').textContent=j?.error||'';$('jobError').classList.toggle('hidden',!j?.error);
  $('notice').textContent=s.notice||'首次使用，在「设置与登录」完成账号连接。';
  const logs=(s.activity&&s.activity!=='release')||!s.job?s.activityLogs||[]:s.job.logs||[];$('logCount').textContent=logs.length+' 条';
  const logHTML=logs.map(e=>`<div class="log-line"><time>${escapeHTML(new Date(e.time).toLocaleTimeString('zh-CN',{hour12:false}))}</time><span>${escapeHTML(e.message)}</span></div>`).join('');
  if($('logs').innerHTML!==logHTML){const bottom=$('logs').scrollHeight-$('logs').scrollTop-$('logs').clientHeight<30;$('logs').innerHTML=logHTML;if(bottom)$('logs').scrollTop=$('logs').scrollHeight;}
  $('refreshButton').disabled=busy||action;
  if(!busy&&lastBusy){refresh().catch(()=>{});}lastBusy=busy;
  for(const [id,check] of [['githubStatus','github'],['gitcodeStatus','gitcode-login'],['lanzouStatus','lanzou-login']]){$(id).textContent=checks.find(c=>c.id===check)?.ok?'已连接 · 登录失效时可重新登录':'尚未确认登录';}
  $('cloudStatus').textContent=s.cloudLoggedIn?'已登录 · 凭据保存在系统钥匙串':'未登录 · 凭据保存在系统钥匙串';$('cloudLogout').classList.toggle('hidden',!s.cloudLoggedIn);
  for(const button of document.querySelectorAll('[data-login],#saveSettings,#cloudLogin,#chooseProject,#cloudLogout'))button.disabled=busy||action;
}
function elapsed(j){const end=j.status==='running'?Date.now():new Date(j.updated).getTime();const seconds=Math.max(0,Math.floor((end-new Date(j.started))/1000));return seconds>=3600?`${Math.floor(seconds/3600)}小时 ${Math.floor(seconds%3600/60)}分`:seconds>=60?`${Math.floor(seconds/60)}分 ${seconds%60}秒`:`${seconds}秒`;}
async function refresh(){const s=await api('Refresh');render(s);return s;}
async function run(fn){if(action)return;action=true;if(state)render(state);try{await fn();render(await api('GetState'));}catch(e){toast(e);$('settingsError').textContent=String(e);}finally{action=false;if(state)render(state);}}
function openSettings(){if(!state)return;const s=state.settings;$('project').value=s.project;$('cloudURL').value=s.cloudURL;$('cloudEnabled').checked=s.cloudEnabled;$('visibleBrowser').checked=s.visibleBrowser;$('settingsError').textContent='';$('settingsDialog').showModal();}
function settings(){return {project:$('project').value,cloudURL:$('cloudURL').value,cloudEnabled:$('cloudEnabled').checked,visibleBrowser:$('visibleBrowser').checked};}
$('settingsButton').onclick=openSettings;$('closeSettings').onclick=()=>$('settingsDialog').close();
$('chooseProject').onclick=()=>run(async()=>{const chosen=await api('ChooseProject');if(chosen)$('project').value=chosen;});
$('projectButton').onclick=()=>state?.settings.project?run(()=>api('Open','project')):openSettings();
$('settingsForm').onsubmit=e=>{e.preventDefault();run(async()=>{await api('SaveSettings',settings());$('settingsDialog').close();await refresh();});};
$('cloudLogin').onclick=()=>run(async()=>{try{await api('SaveSettings',settings());await api('LoginCloud',$('cloudURL').value,$('username').value,$('password').value);$('cloudEnabled').checked=true;await refresh();toast('云端已连接，后续发布将自动联动');}finally{$('password').value='';}});
$('cloudLogout').onclick=()=>run(async()=>{await api('LogoutCloud');await refresh();});
$('refreshButton').onclick=()=>run(refresh);
$('startButton').onclick=()=>run(async()=>{await api('Start',$('notes').value,resumable(state));forceNew=false;selectedKey='';});
$('stopButton').onclick=()=>run(()=>api('Stop'));
$('history').onchange=()=>run(async()=>{if(!$('history').value){forceNew=true;selectedKey='';render(state);}else{forceNew=false;await api('Select',$('history').value);}});
$('newJobButton').onclick=()=>{forceNew=true;selectedKey='';render(state);};
document.addEventListener('click',e=>{const b=e.target.closest('button');if(!b)return;if(b.dataset.open)run(()=>api('Open',b.dataset.open));if(b.dataset.settings)openSettings();if(b.dataset.login)run(async()=>{if($('settingsDialog').open){await api('SaveSettings',settings());$('settingsDialog').close();}await api('Login',b.dataset.login);toast('请在打开的浏览器中完成登录。');});});
async function init(){try{render(await api('GetState'));await refresh();setInterval(async()=>{if(polling||action)return;polling=true;try{render(await api('GetState'));}finally{polling=false;}},1000);}catch(e){toast('发布器启动检查失败：'+e);$('startButton').textContent='打开设置';$('startButton').disabled=false;$('startButton').onclick=openSettings;}}
init();
