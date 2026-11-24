(() => {
  const CSS = `
  .setbox{max-width:568px}
  .sethead-t{display:inline-flex;align-items:center;gap:10px}
  .sethead-t svg{width:22px;height:22px;color:var(--accent,#0b57d0);flex:none}
  .setcard{background:var(--panel,#f0f4f9);border:1px solid var(--line,rgba(68,71,70,.15));border-radius:18px;padding:14px 16px;margin:12px 0}
  .setrow{margin:10px 0}
  .setcard .setrow:first-child{margin-top:0}
  .setcard .setrow:last-child{margin-bottom:0}
  .setrow label.lbl{display:block;font-size:12px;font-weight:600;color:var(--dim,#444746);margin-bottom:6px;letter-spacing:.01em}
  .setdir{display:flex;gap:8px;align-items:center}
  .setdir input{flex:1;min-width:0;height:40px;padding:0 12px;border-radius:12px;border:1px solid var(--line-strong,var(--line,rgba(68,71,70,.25)));background:var(--input-bg,var(--surface-card,#fff));color:var(--fg,#1f1f1f);font:12px/1.3 ui-monospace,Consolas,monospace;outline:none;transition:border-color .16s,box-shadow .16s}
  .setdir input:focus{border-color:var(--accent,#0b57d0);box-shadow:0 0 0 3px var(--accent-soft,rgba(11,87,208,.18))}
  .setbtn{height:38px;border:1px solid var(--line,rgba(68,71,70,.18));background:var(--panel2,#e9eef6);color:var(--fg,#1f1f1f);border-radius:999px;padding:0 16px;font-size:12.5px;font-weight:600;cursor:pointer;white-space:nowrap;display:inline-flex;align-items:center;justify-content:center;gap:6px;transition:background .15s,filter .15s,box-shadow .15s,transform .12s}
  .setbtn:hover{background:var(--panel3,#dde3ea)}
  .setbtn:active{transform:scale(.97)}
  .setbtn.pri{background:var(--accent,#0b57d0);color:var(--on-accent,#fff);border-color:transparent;box-shadow:var(--shadow-1,0 1px 2px rgba(0,0,0,.1))}
  .setbtn.pri:hover{background:var(--accent,#0b57d0);filter:brightness(1.06);box-shadow:var(--shadow-2,0 2px 6px rgba(0,0,0,.14))}
  .setgrid{display:grid;grid-template-columns:1fr 1fr;gap:12px;margin-top:12px}
  .setgrid .setrow{margin:0}
  .setrow select{width:100%;height:40px;padding:0 12px;border-radius:12px;border:1px solid var(--line-strong,var(--line,rgba(68,71,70,.25)));background:var(--input-bg,var(--surface-card,#fff));color:var(--fg,#1f1f1f);font-size:13px;outline:none;cursor:pointer;transition:border-color .16s}
  .setrow select:focus{border-color:var(--accent,#0b57d0)}
  .setchk{display:flex;align-items:center;justify-content:space-between;gap:12px;font-size:13px;font-weight:500;cursor:pointer;padding:8px 2px;margin:2px 0;color:var(--fg,#1f1f1f);user-select:none}
  .setchk input{appearance:none;-webkit-appearance:none;position:relative;width:44px;height:26px;border-radius:999px;background:var(--panel3,#dde3ea);border:1.5px solid var(--muted,#747775);cursor:pointer;flex:none;order:2;transition:background .18s cubic-bezier(.2,0,0,1),border-color .18s}
  .setchk input::after{content:"";position:absolute;left:4px;top:50%;width:14px;height:14px;border-radius:50%;background:var(--muted,#747775);transform:translateY(-50%);transition:transform .18s cubic-bezier(.2,0,0,1),width .18s,height .18s,background .18s}
  .setchk input:checked{background:var(--accent,#0b57d0);border-color:var(--accent,#0b57d0)}
  .setchk input:checked::after{width:18px;height:18px;background:var(--on-accent,#fff);transform:translate(17px,-50%)}
  .setswitches{display:flex;flex-direction:column;gap:2px;margin-top:10px;padding-top:8px;border-top:1px solid var(--line,rgba(68,71,70,.15))}
  .seterr{font-size:12.5px;font-weight:500;color:var(--err,#b3261e);margin-top:8px;min-height:18px}
  .sethint{font-size:12px;color:var(--dim,#444746);line-height:1.55;margin-top:6px}
  .setnote{background:var(--accent-soft,#d3e3fd);color:var(--on-accent-soft,#041e49);border-radius:14px;padding:10px 14px;font-size:12.5px;line-height:1.55;margin:10px 0 4px}
  .setwarn{background:var(--warn-soft,#feefc3);color:var(--on-warn-soft,#412100);border-radius:12px;padding:9px 12px;font-size:12px;line-height:1.55;margin-top:8px}
  .setfoot{display:flex;justify-content:flex-end;gap:10px;margin-top:14px;padding-top:12px;border-top:1px solid var(--line,rgba(68,71,70,.15))}
  @media (max-width:520px){.setgrid{grid-template-columns:1fr}.setdir{flex-wrap:wrap}}
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
    modal.innerHTML = `<div class="login setbox">
      <h2><span class="sethead-t"><svg viewBox="0 0 24 24"><path d="M19.14 12.94c.04-.3.06-.61.06-.94 0-.32-.02-.64-.07-.94l2.03-1.58a.49.49 0 0 0 .12-.61l-1.92-3.32a.49.49 0 0 0-.59-.22l-2.39.96c-.5-.38-1.03-.7-1.62-.94l-.36-2.54a.484.484 0 0 0-.48-.41h-3.84c-.24 0-.43.17-.47.41l-.36 2.54c-.59.24-1.13.57-1.62.94l-2.39-.96a.49.49 0 0 0-.59.22L2.74 8.87c-.12.21-.08.47.12.61l2.03 1.58c-.05.3-.09.63-.09.94s.02.64.07.94l-2.03 1.58a.49.49 0 0 0-.12.61l1.92 3.32c.12.22.37.29.59.22l2.39-.96c.5.38 1.03.7 1.62.94l.36 2.54c.05.24.24.41.48.41h3.84c.24 0 .44-.17.47-.41l.36-2.54c.59-.24 1.13-.56 1.62-.94l2.39.96c.22.08.47 0 .59-.22l1.92-3.32c.12-.22.07-.47-.12-.61l-2.01-1.58zM12 15.6A3.6 3.6 0 1 1 12 8.4a3.6 3.6 0 0 1 0 7.2z" fill="currentColor"/></svg><span>播放器与下载设置</span></span><button class="xbtn" id="setClose" type="button" aria-label="关闭">×</button></h2>
      <div id="setDesktopSec" class="setcard">
        <div class="setrow">
          <label class="lbl">默认下载目录（桌面独立窗口版）</label>
          <div class="setdir">
            <input id="setDirInput" type="text" placeholder="自动使用安全可写目录（如 视频 / dist\\downloads）">
            <button class="setbtn" id="setPickDir" type="button">选择目录</button>
            <button class="setbtn" id="setOpenDir" type="button">打开</button>
          </div>
          <div class="sethint" id="setDirEff"></div>
          <div class="setwarn" id="setLowIlWarn" style="display:none"></div>
        </div>
        <label class="setchk"><span>每次下载前弹窗「另存为」</span><input id="setAskSave" type="checkbox"></label>
      </div>
      <div id="setBrowserNote" class="setnote" style="display:none">
        当前为浏览器模式，下载路径由浏览器自身设置管理；播放偏好对老/新版页面同时生效。
      </div>
      <div class="setcard">
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
        </div>
        <div class="setswitches">
          <label class="setchk"><span>自动连播下一讲</span><input id="setAutoNext" type="checkbox"></label>
          <label class="setchk"><span>记住上次播放位置</span><input id="setRememberPos" type="checkbox"></label>
        </div>
      </div>
      <div class="seterr" id="setErr"></div>
      <div class="setfoot">
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
    const androidApp = !desk && !!(window.yhktApp && window.yhktApp.login);
    modal.querySelector('#setDesktopSec').style.display = desk ? '' : 'none';
    modal.querySelector('#setResetDir').style.display = desk ? '' : 'none';
    const noteEl = modal.querySelector('#setBrowserNote');
    noteEl.style.display = desk ? 'none' : '';
    if (androidApp) {
      noteEl.textContent = '当前为手机应用模式，视频默认保存至系统「内部存储/Download」（可在「下载管理」查看与播放）；播放偏好对老/新版页面同时生效。';
    } else {
      noteEl.textContent = '当前为浏览器模式，下载路径由浏览器自身设置管理；播放偏好对老/新版页面同时生效。';
    }
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
