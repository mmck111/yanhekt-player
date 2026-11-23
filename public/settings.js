(() => {
  const CSS = `
  .setrow{margin:10px 0}
  .setrow label.lbl{display:block;font-size:12px;color:var(--dim,#8f9db3);margin-bottom:5px}
  .setdir{display:flex;gap:6px;align-items:center}
  .setdir input{flex:1;min-width:0;padding:8px 10px;border-radius:8px;border:1px solid var(--line,rgba(255,255,255,.1));background:var(--panel2,#171c26);color:var(--fg,#edf2f7);font:12.5px/1.3 monospace}
  .setbtn{border:1px solid var(--line,rgba(255,255,255,.12));background:var(--panel2,#171c26);color:var(--fg,#edf2f7);border-radius:8px;padding:7px 10px;font-size:12px;cursor:pointer;white-space:nowrap}
  .setbtn:hover{border-color:rgba(91,156,255,.5);color:#fff}
  .setbtn.pri{background:var(--accent,#5b9cff);color:#041022;border-color:transparent;font-weight:700}
  .setgrid{display:grid;grid-template-columns:1fr 1fr;gap:10px}
  .setrow select{width:100%;padding:8px 10px;border-radius:8px;border:1px solid var(--line,rgba(255,255,255,.1));background:var(--panel2,#171c26);color:var(--fg,#edf2f7);font-size:12.5px}
  .setchk{display:flex;align-items:center;gap:8px;font-size:12.5px;cursor:pointer;margin:8px 0;color:var(--fg,#edf2f7)}
  .setchk input{width:15px;height:15px;accent-color:var(--accent,#5b9cff)}
  .seterr{font-size:12px;color:#fca5a5;margin-top:6px;min-height:16px}
  .sethint{font-size:11.5px;color:var(--dim,#8f9db3);line-height:1.45;margin-top:4px}
  @media (max-width:520px){.setgrid{grid-template-columns:1fr}}
  `;

  let modal = null;
  let state = {
    downloadDir: '',
    askSavePath: false,
    defaultView: 'camera',
    dualPip: false,
    defaultRate: 1,
    autoNext: false,
    rememberPos: true,
  };
  let effectiveDir = '';
  let isDesktop = false;
  let isLowIL = false;
  let onApplyCb = null;
  let toastFn = m => { try { if (typeof toast === 'function') toast(m, 2600); } catch(e){} };

  function ensureUI(){
    if (modal) return;
    const st = document.createElement('style');
    st.textContent = CSS;
    document.head.appendChild(st);

    modal = document.createElement('div');
    modal.id = 'setModal';
    modal.className = 'lgmodal';
    modal.innerHTML = `<div class="login" style="max-width:500px">
      <h2><span>播放器与下载设置</span><button class="xbtn" id="setClose" type="button">×</button></h2>
      <div id="setDesktopSec">
        <div class="setrow">
          <label class="lbl">默认下载目录（桌面独立窗口版）</label>
          <div class="setdir">
            <input id="setDirInput" type="text" placeholder="自动使用安全可写目录（如 视频 / dist\\downloads）">
            <button class="setbtn" id="setPickDir" type="button">选择目录</button>
            <button class="setbtn" id="setOpenDir" type="button">打开</button>
          </div>
          <div class="sethint" id="setDirEff"></div>
          <div class="sethint" id="setLowIlWarn" style="display:none;color:#fbbf24"></div>
        </div>
        <label class="setchk"><input id="setAskSave" type="checkbox"> 每次下载前弹窗「另存为」（不勾选则直接保存到默认目录，无权限弹窗）</label>
      </div>
      <div id="setBrowserNote" class="sethint" style="display:none;margin-bottom:8px">
        当前为浏览器模式，下载路径由浏览器自身设置管理；播放偏好对老/新版页面同时生效。
      </div>
      <div class="setgrid">
        <div class="setrow">
          <label class="lbl">默认播放视角</label>
          <select id="setView">
            <option value="camera">教师 / 全景画面</option>
            <option value="dual">双画同步（自动回落单画）</option>
            <option value="screen">课件 / 电脑屏幕</option>
            <option value="room">教室视角（仅新版/直播）</option>
          </select>
        </div>
        <div class="setrow">
          <label class="lbl">双画默认布局</label>
          <select id="setDualPip">
            <option value="0">并排双屏</option>
            <option value="1">画中画</option>
          </select>
        </div>
        <div class="setrow">
          <label class="lbl">默认倍速</label>
          <select id="setRate">
            <option value="0.75">0.75x</option>
            <option value="1">1.0x</option>
            <option value="1.25">1.25x</option>
            <option value="1.5">1.5x</option>
            <option value="1.75">1.75x</option>
            <option value="2">2.0x</option>
          </select>
        </div>
        <div class="setrow" style="display:flex;flex-direction:column;justify-content:flex-end">
          <label class="setchk" style="margin:4px 0"><input id="setAutoNext" type="checkbox"> 自动连播下一讲</label>
          <label class="setchk" style="margin:4px 0"><input id="setRememberPos" type="checkbox"> 记住上次播放位置</label>
        </div>
      </div>
      <div class="seterr" id="setErr"></div>
      <div style="display:flex;justify-content:flex-end;gap:8px;margin-top:10px">
        <button class="setbtn" id="setResetDir" type="button">恢复默认目录</button>
        <button class="setbtn pri" id="setSaveBtn" type="button">保存并应用</button>
      </div>
    </div>`;
    document.body.appendChild(modal);

    modal.addEventListener('click', e => { if (e.target === modal) closeModal(); });
    modal.querySelector('#setClose').onclick = closeModal;
    modal.querySelector('#setPickDir').onclick = () => {
      modal.querySelector('#setErr').textContent = '';
      if (window.yhktApp && window.yhktApp.pickDownloadDir) {
        window.yhktApp.pickDownloadDir();
      }
    };
    modal.querySelector('#setOpenDir').onclick = () => {
      const target = modal.querySelector('#setDirInput').value.trim() || effectiveDir;
      if (window.yhktApp && window.yhktApp.openDownloadDir) {
        window.yhktApp.openDownloadDir(target);
      }
    };
    modal.querySelector('#setResetDir').onclick = async () => {
      modal.querySelector('#setDirInput').value = '';
      await submitSettings(true);
    };
    modal.querySelector('#setSaveBtn').onclick = () => submitSettings(false);
  }

  function renderForm(){
    if (!modal) return;
    const desk = !!(isDesktop || (window.yhktApp && window.yhktApp.isDesktop));
    modal.querySelector('#setDesktopSec').style.display = desk ? '' : 'none';
    modal.querySelector('#setResetDir').style.display = desk ? '' : 'none';
    modal.querySelector('#setBrowserNote').style.display = desk ? 'none' : '';
    modal.querySelector('#setDirInput').value = state.downloadDir || '';
    modal.querySelector('#setDirEff').textContent = effectiveDir ? ('当前实际保存目录：' + effectiveDir) : '';
    const lowEl = modal.querySelector('#setLowIlWarn');
    if (desk && isLowIL) {
      lowEl.style.display = '';
      lowEl.textContent = '⚠ 当前 exe 正在受限的低完整性目录下运行，系统会拦截写入桌面/下载/视频目录；通过安装包安装或复制到普通文件夹后即可保存到任意目录。';
    } else {
      lowEl.style.display = 'none';
    }
    modal.querySelector('#setAskSave').checked = !!state.askSavePath;
    modal.querySelector('#setView').value = state.defaultView || 'camera';
    modal.querySelector('#setDualPip').value = state.dualPip ? '1' : '0';
    modal.querySelector('#setRate').value = String(state.defaultRate || 1);
    modal.querySelector('#setAutoNext').checked = !!state.autoNext;
    modal.querySelector('#setRememberPos').checked = state.rememberPos !== false;
  }

  async function submitSettings(keepOpen){
    const errEl = modal.querySelector('#setErr');
    errEl.textContent = '';
    const payload = {
      downloadDir: modal.querySelector('#setDirInput').value.trim(),
      askSavePath: modal.querySelector('#setAskSave').checked,
      defaultView: modal.querySelector('#setView').value,
      dualPip: modal.querySelector('#setDualPip').value === '1',
      defaultRate: Number(modal.querySelector('#setRate').value) || 1,
      autoNext: modal.querySelector('#setAutoNext').checked,
      rememberPos: modal.querySelector('#setRememberPos').checked,
    };
    try {
      const r = await fetch('/api/settings', {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify(payload),
      });
      const j = await r.json();
      if (!r.ok || !j.ok) {
        errEl.textContent = (j && j.error) || ('保存失败 HTTP ' + r.status);
        return;
      }
      state = j.settings || payload;
      effectiveDir = j.effectiveDownloadDir || '';
      isDesktop = !!j.desktop;
      renderForm();
      if (onApplyCb) onApplyCb(state);
      toastFn('设置已保存并生效');
      if (!keepOpen) closeModal();
    } catch (e) {
      errEl.textContent = '请求失败：' + (e && e.message ? e.message : String(e));
    }
  }

  async function openModal(){
    ensureUI();
    await load();
    modal.querySelector('#setErr').textContent = '';
    renderForm();
    modal.style.display = 'flex';
  }

  function closeModal(){
    if (modal) modal.style.display = 'none';
  }

  async function load(){
    try {
      const r = await fetch('/api/settings');
      const j = await r.json();
      if (j && j.ok && j.settings) {
        state = j.settings;
        effectiveDir = j.effectiveDownloadDir || '';
        isDesktop = !!j.desktop;
        isLowIL = !!j.lowIntegrity;
      }
    } catch (e) {}
    return state;
  }

  window.__yhktOnDownloadDirPicked = res => {
    if (!modal) return;
    const errEl = modal.querySelector('#setErr');
    if (!res || !res.ok) {
      errEl.textContent = (res && res.error) || '所选目录不可写';
      return;
    }
    errEl.textContent = '';
    state.downloadDir = res.dir || '';
    effectiveDir = res.dir || effectiveDir;
    modal.querySelector('#setDirInput').value = state.downloadDir;
    modal.querySelector('#setDirEff').textContent = '当前实际保存目录：' + effectiveDir;
    toastFn('已更新默认下载目录：' + state.downloadDir);
  };

  window.yhktSettings = {
    get: () => state,
    isOpen: () => !!(modal && modal.style.display === 'flex'),
    open: openModal,
    close: closeModal,
    async init(opts){
      if (opts && typeof opts.onApply === 'function') onApplyCb = opts.onApply;
      if (opts && typeof opts.toast === 'function') toastFn = opts.toast;
      const btn = document.getElementById('btnSet');
      if (btn) btn.onclick = e => { e.preventDefault(); if (opts && opts.onBeforeOpen) opts.onBeforeOpen(); openModal(); };
      const s = await load();
      if (onApplyCb) onApplyCb(s);
      return s;
    },
  };
})();
