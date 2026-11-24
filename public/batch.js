(() => {
  const CSS = `
  .bdbox{width:min(900px,96vw);max-height:90vh;display:flex;flex-direction:column;background:var(--panel,#111622);border:1px solid var(--line,rgba(255,255,255,.1));border-radius:16px;padding:18px 20px;box-shadow:0 24px 64px rgba(0,0,0,.65);color:var(--fg,#edf2f7)}
  .bdhead{display:flex;align-items:center;justify-content:space-between;gap:10px;padding-bottom:10px;border-bottom:1px solid var(--line,rgba(255,255,255,.08))}
  .bdhead h2{margin:0;font-size:16.5px;display:flex;align-items:center;gap:8px}
  .bdhead .bdacts{display:flex;align-items:center;gap:8px}
  .bdgrid{display:grid;grid-template-columns:1.02fr 1.18fr;gap:14px;min-height:0;flex:1;margin-top:12px;overflow:hidden}
  .bdcol{display:flex;flex-direction:column;min-height:0;border:1px solid var(--line,rgba(255,255,255,.08));border-radius:12px;background:var(--panel2,#171c26);padding:12px}
  .bdbar{display:flex;gap:6px;flex-wrap:wrap;margin-bottom:8px}
  .bdbar input,.bdbar select{background:var(--panel,#111622);border:1px solid var(--line,rgba(255,255,255,.12));color:var(--fg,#edf2f7);border-radius:8px;padding:7px 10px;font-size:12.5px;outline:none}
  .bdbar input{flex:1;min-width:140px}
  .bdbar input:focus,.bdbar select:focus{border-color:var(--accent,#5b9cff)}
  .bdbtn{border:1px solid var(--line,rgba(255,255,255,.14));background:var(--panel3,#1e2533);color:var(--fg,#edf2f7);border-radius:8px;padding:7px 12px;font-size:12.5px;font-weight:600;cursor:pointer;white-space:nowrap}
  .bdbtn:hover:not(:disabled){border-color:rgba(91,156,255,.6);color:#fff}
  .bdbtn:disabled{opacity:.42;cursor:not-allowed}
  .bdbtn.pri{background:var(--accent,#5b9cff);border-color:var(--accent,#5b9cff);color:#041022;font-weight:700}
  .bdbtn.pri:hover:not(:disabled){filter:brightness(1.08);color:#041022}
  .bdlist{flex:1;min-height:220px;max-height:48vh;overflow:auto;display:flex;flex-direction:column;gap:5px;padding-right:2px}
  .bdrow{padding:8px 10px;border-radius:9px;border:1px solid transparent;background:rgba(255,255,255,.025);cursor:pointer;transition:all .12s}
  .bdrow:hover{background:rgba(255,255,255,.06);border-color:rgba(255,255,255,.1)}
  .bdrow.on{background:rgba(91,156,255,.16);border-color:rgba(91,156,255,.55)}
  .bdrow .t{font-size:13px;font-weight:600;line-height:1.4;display:flex;align-items:center;gap:6px;flex-wrap:wrap}
  .bdrow .m{font-size:11px;color:var(--dim,#8f9db3);margin-top:3px;display:flex;gap:6px;flex-wrap:wrap;align-items:center}
  .bdtag{font-size:10.5px;padding:1px 6px;border-radius:5px;border:1px solid rgba(255,255,255,.12);color:var(--dim,#8f9db3);background:rgba(255,255,255,.04)}
  .bdtag.y2{color:#8be9be;border-color:rgba(52,211,153,.35);background:rgba(52,211,153,.1)}
  .bdtag.old{color:#fde68a;border-color:rgba(251,191,36,.32);background:rgba(251,191,36,.1)}
  .bdtoolbar{display:flex;align-items:center;gap:6px;flex-wrap:wrap;margin-bottom:8px;padding-bottom:8px;border-bottom:1px solid var(--line,rgba(255,255,255,.08))}
  .bdsess{display:flex;align-items:flex-start;gap:9px;padding:8px 9px;border-radius:9px;border:1px solid transparent;background:rgba(255,255,255,.025);cursor:pointer}
  .bdsess:hover:not(.dead){background:rgba(255,255,255,.06)}
  .bdsess.dead{opacity:.42;cursor:not-allowed}
  .bdsess input[type=checkbox]{width:16px;height:16px;margin-top:2px;accent-color:var(--accent,#5b9cff);cursor:pointer;flex:none}
  .bdsess .info{flex:1;min-width:0}
  .bdsess .st{font-size:12.5px;font-weight:600;line-height:1.4}
  .bdsess .sm{font-size:11px;color:var(--dim,#8f9db3);margin-top:2px;display:flex;gap:6px;flex-wrap:wrap}
  .bdfoot{display:flex;align-items:center;justify-content:space-between;gap:10px;flex-wrap:wrap;margin-top:12px;padding-top:10px;border-top:1px solid var(--line,rgba(255,255,255,.08))}
  .bdfoot .dirinfo{font-size:11.5px;color:var(--dim,#8f9db3);display:flex;align-items:center;gap:8px;flex-wrap:wrap;min-width:0}
  .bdfoot .dirinfo a{color:var(--accent,#5b9cff);cursor:pointer;text-decoration:none}
  .bdempty{margin:auto;padding:22px 12px;text-align:center;color:var(--dim,#8f9db3);font-size:12.5px;line-height:1.6}
  @media (max-width:740px){
    .bdgrid{grid-template-columns:1fr;overflow:auto}
    .bdlist{max-height:28vh;min-height:140px}
  }
  `;

  let modal = null;
  let optsCfg = {};
  let searchSeq = 0;
  let activeCourse = null; // { platform: 'y2'|'old', id, title, teacher, term }
  let sessions = []; // [{ id, title, date, subText, duration, hasMain, hasVga, playable, checked }]
  let browserQueue = [];
  let browserPumping = false;

  const esc = s => String(s == null ? '' : s).replace(/[&<>"]/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c]));
  const toastMsg = (m, ms) => {
    try { if (optsCfg.toast) optsCfg.toast(m, ms || 2800); else if (typeof toast === 'function') toast(m, ms || 2800); } catch (e) {}
  };

  function ensureUI() {
    if (modal) return;
    const st = document.createElement('style');
    st.textContent = CSS;
    document.head.appendChild(st);

    modal = document.createElement('div');
    modal.id = 'batchDlModal';
    modal.className = 'lgmodal';
    modal.innerHTML = `<div class="bdbox">
      <div class="bdhead">
        <h2>📦 批量搜课下载专区</h2>
        <div class="bdacts">
          <button class="bdbtn" id="bdGoQueue" type="button">⤓ 查看下载任务</button>
          <button class="xbtn" id="bdClose" type="button" style="background:var(--panel2,#171c26);border:1px solid var(--line,rgba(255,255,255,.1));color:var(--dim,#8f9db3);border-radius:8px;width:28px;height:28px;cursor:pointer;font-size:15px">×</button>
        </div>
      </div>
      <div class="bdgrid">
        <div class="bdcol">
          <div class="bdbar">
            <input id="bdSearchInp" placeholder="搜课程名 / 教师名（空格分隔多关键词）" autocomplete="off">
            <button class="bdbtn pri" id="bdSearchBtn" type="button">搜课</button>
          </div>
          <div class="bdbar">
            <select id="bdPlat" style="flex:0 0 auto">
              <option value="all">全部平台</option>
              <option value="y2">新版平台（快 / MP4）</option>
              <option value="old">老平台（历史全量）</option>
            </select>
            <select id="bdTerm" style="flex:1;min-width:110px">
              <option value="">全部学期</option>
            </select>
          </div>
          <div class="bdlist" id="bdCourseList">
            <div class="bdempty">输入课程名或教师名搜课，然后在右侧勾选要下载的讲次（支持全选整门课程一起下载）。</div>
          </div>
        </div>
        <div class="bdcol">
          <div id="bdCourseHead" style="margin-bottom:8px">
            <div id="bdCourseTitle" style="font-size:14px;font-weight:700">未选择课程</div>
            <div id="bdCourseMeta" style="font-size:11.5px;color:var(--dim,#8f9db3);margin-top:2px">点击左侧任意课程即可加载全部讲次</div>
          </div>
          <div class="bdtoolbar">
            <button class="bdbtn" id="bdSelAll" type="button" disabled>☑ 全选本课所有讲</button>
            <button class="bdbtn" id="bdSelNone" type="button" disabled>☐ 清空</button>
            <select id="bdViewMode" title="批量下载选择的画面视角" style="margin-left:auto;background:var(--panel,#111622);border:1px solid var(--line,rgba(255,255,255,.12));color:var(--fg,#edf2f7);border-radius:8px;padding:5px 8px;font-size:12px">
              <option value="camera">下载：教师 / 全景画面</option>
              <option value="screen">下载：课件画面</option>
              <option value="both">下载：教师 + 课件（分别保存 2 个文件）</option>
              <option value="dual" data-ffmpeg-only="1" hidden disabled>下载：双画面（本机 ffmpeg 同屏合成单个 .mp4）</option>
            </select>
          </div>
          <div class="bdlist" id="bdSessionList">
            <div class="bdempty">等待选择课程…</div>
          </div>
        </div>
      </div>
      <div class="bdfoot">
        <div class="dirinfo">
          <span id="bdSaveHint">正在读取保存位置…</span>
          <a id="bdOpenSet">修改保存目录 / 设置</a>
        </div>
        <div style="display:flex;align-items:center;gap:10px">
          <span id="bdCountTxt" style="font-size:12px;color:var(--dim,#8f9db3)">已选 0 讲</span>
          <button class="bdbtn pri" id="bdStartBtn" type="button" disabled>⤓ 批量开始下载</button>
        </div>
      </div>
    </div>`;
    document.body.appendChild(modal);

    modal.addEventListener('click', e => { if (e.target === modal) closeModal(); });
    modal.querySelector('#bdClose').onclick = closeModal;
    modal.querySelector('#bdGoQueue').onclick = () => {
      closeModal();
      if (optsCfg.openDownloads) optsCfg.openDownloads();
    };
    modal.querySelector('#bdSearchBtn').onclick = doSearch;
    modal.querySelector('#bdSearchInp').addEventListener('keydown', e => { if (e.key === 'Enter') doSearch(); });
    modal.querySelector('#bdPlat').onchange = () => { if (modal.querySelector('#bdSearchInp').value.trim()) doSearch(); };
    modal.querySelector('#bdTerm').onchange = () => { if (modal.querySelector('#bdSearchInp').value.trim() || modal.querySelector('#bdTerm').value) doSearch(); };
    modal.querySelector('#bdSelAll').onclick = () => toggleAll(true);
    modal.querySelector('#bdSelNone').onclick = () => toggleAll(false);
    modal.querySelector('#bdViewMode').onchange = updateCounts;
    modal.querySelector('#bdStartBtn').onclick = startBatchDownload;
    modal.querySelector('#bdOpenSet').onclick = () => {
      closeModal();
      if (window.yhktSettings) window.yhktSettings.open();
    };
  }

  async function refreshSaveHint() {
    const el = modal && modal.querySelector('#bdSaveHint');
    if (!el) return;
    try {
      const j = await (await fetch('/api/settings')).json();
      if (j && j.desktop) {
        const d = j.effectiveDownloadDir || (j.settings && j.settings.downloadDir) || '默认下载目录';
        const ask = j.settings && j.settings.askSavePath;
        el.textContent = ask ? '保存位置：下载前选择一次文件夹' : ('自动保存到：' + d);
      } else {
        el.textContent = '浏览器模式：按浏览器默认下载目录排队保存';
      }
    } catch (e) { el.textContent = ''; }
  }

  let termsLoaded = false;
  async function loadTerms() {
    if (termsLoaded || !modal) return;
    const sel = modal.querySelector('#bdTerm');
    const seen = new Set();
    try {
      const r1 = await fetch('/api/y2/terms').then(r => r.json()).catch(() => null);
      if (r1 && r1.ok && r1.terms) {
        for (const t of r1.terms) {
          if (!t.name || seen.has(t.name)) continue;
          seen.add(t.name);
          const o = document.createElement('option');
          o.value = t.id + '|' + t.name;
          o.textContent = t.name;
          sel.appendChild(o);
        }
      }
      const r2 = await fetch('/api/db/terms').then(r => r.json()).catch(() => null);
      if (r2 && r2.ok && r2.terms) {
        for (const name of r2.terms) {
          if (!name || seen.has(name)) continue;
          seen.add(name);
          const o = document.createElement('option');
          o.value = '|' + name;
          o.textContent = name;
          sel.appendChild(o);
        }
      }
      termsLoaded = true;
    } catch (e) {}
  }

  async function doSearch() {
    const q = modal.querySelector('#bdSearchInp').value.trim();
    const plat = modal.querySelector('#bdPlat').value;
    const termRaw = modal.querySelector('#bdTerm').value;
    const sepIdx = termRaw.indexOf('|');
    const termId = sepIdx >= 0 ? termRaw.slice(0, sepIdx) : '';
    const termName = sepIdx >= 0 ? termRaw.slice(sepIdx + 1) : '';
    if (!q && !termName) {
      toastMsg('先输入关键词或选择学期再搜课');
      return;
    }
    const seq = ++searchSeq;
    const box = modal.querySelector('#bdCourseList');
    box.dataset.fromCur = '0';
    box.innerHTML = '<div class="bdempty">正在检索课程…</div>';

    const jobs = [];
    if (plat !== 'old') {
      const qs = 'q=' + encodeURIComponent(q) + (termId ? '&term=' + encodeURIComponent(termId) : '') + '&limit=30';
      jobs.push(fetch('/api/y2/search?' + qs).then(r => r.json()).catch(() => ({ ok: false, courses: [] })));
    } else {
      jobs.push(Promise.resolve({ ok: true, courses: [] }));
    }
    if (plat !== 'y2') {
      const qs = 'q=' + encodeURIComponent(q) + (termName ? '&term=' + encodeURIComponent(termName) : '') + '&probe=0&limit=40';
      jobs.push(fetch('/api/db/search?' + qs).then(r => r.json()).catch(() => ({ ok: false, courses: [] })));
    } else {
      jobs.push(Promise.resolve({ ok: true, courses: [] }));
    }

    const [y2Res, oldRes] = await Promise.all(jobs);
    if (seq !== searchSeq) return;

    const merged = [];
    if (y2Res && y2Res.courses) {
      for (const c of y2Res.courses) {
        merged.push({
          platform: 'y2',
          id: String(c.courseId),
          title: c.title || ('课程 ' + c.courseId),
          teacher: c.teacher || '',
          term: c.termName || '',
          videoCount: c.videoCount || 0,
          totalSessions: c.totalSessions || c.sessions || 0,
          raw: c,
        });
      }
    }
    if (oldRes && oldRes.courses) {
      for (const c of oldRes.courses) {
        merged.push({
          platform: 'old',
          id: String(c.id),
          title: c.name || ('课程 ' + c.id),
          teacher: c.teacher || '',
          term: c.term || '',
          videoCount: c.videoCount || 0,
          code: c.code || '',
          raw: c,
        });
      }
    }

    box.innerHTML = '';
    if (!merged.length) {
      box.innerHTML = '<div class="bdempty">没有找到匹配的课程，可换个关键词或把学期设为「全部学期」。</div>';
      return;
    }

    for (const item of merged) {
      const row = document.createElement('div');
      row.className = 'bdrow' + (activeCourse && activeCourse.platform === item.platform && activeCourse.id === item.id ? ' on' : '');
      row.innerHTML = `<div class="t"><span class="bdtag ${item.platform}">${item.platform === 'y2' ? '新版' : '老平台'}</span><span>${esc(item.title)}</span></div>`
        + `<div class="m">${item.term ? `<span>${esc(item.term)}</span>` : ''}${item.teacher ? `<span>${esc(item.teacher)}</span>` : ''}${item.videoCount ? `<span class="bdtag y2">${item.videoCount} 讲有录像</span>` : ''}</div>`;
      row.onclick = () => {
        box.querySelectorAll('.bdrow').forEach(x => x.classList.remove('on'));
        row.classList.add('on');
        loadCourseSessions(item);
      };
      box.appendChild(row);
    }
  }

  function mapY2Session(s, idx) {
    return {
      id: String(s.subId),
      date: s.date || '',
      title: (s.date || '—') + (s.period ? ' ' + s.period : '') + (s.title && s.title !== s.period ? ' · ' + s.title : ''),
      subText: [s.duration ? Math.round(s.duration / 60) + ' 分钟' : '', s.room || ''].filter(Boolean).join(' · '),
      hasMain: true,
      hasVga: true,
      playable: true,
      checked: false,
      idx,
    };
  }

  function mapOldSession(s, idx) {
    const hm = !!s.main;
    const hv = !!s.vga;
    return {
      id: String(s.id),
      date: s.date || '',
      title: (s.date || '—') + (s.week ? ' 第' + s.week + '周' : '') + (s.title ? ' · ' + s.title : ''),
      subText: [
        s.duration ? Math.round(s.duration / 60) + ' 分钟' : (hm || hv ? '可播' : '无录像'),
        hv && hm ? '全景+课件' : (hm ? '仅全景' : (hv ? '仅课件' : '')),
        s.location || '',
      ].filter(Boolean).join(' · '),
      hasMain: hm,
      hasVga: hv,
      playable: hm || hv,
      checked: false,
      idx,
    };
  }

  async function loadCourseSessions(course) {
    activeCourse = course;
    sessions = [];
    const titleEl = modal.querySelector('#bdCourseTitle');
    const metaEl = modal.querySelector('#bdCourseMeta');
    const sBox = modal.querySelector('#bdSessionList');
    titleEl.textContent = (course.platform === 'y2' ? '[新版] ' : '[老平台] ') + course.title;
    metaEl.textContent = [course.teacher, course.term].filter(Boolean).join(' · ') || '正在读取讲次…';
    modal.querySelector('#bdSelAll').disabled = true;
    modal.querySelector('#bdSelNone').disabled = true;
    updateCounts();

    if (Array.isArray(course.sessions) && course.sessions.length > 0) {
      sessions = course.sessions.map((s, idx) => (course.platform === 'y2' ? mapY2Session(s, idx) : mapOldSession(s, idx)));
      renderSessions();
      return;
    }

    sBox.innerHTML = '<div class="bdempty">正在读取课程讲次列表…</div>';

    if (course.platform === 'y2') {
      try {
        const st = await fetch('/api/y2/status').then(r => r.json());
        if (!st || !st.loggedIn) {
          sBox.innerHTML = `<div class="bdempty">需要登录新版平台后才能读取讲次列表。<br><button class="bdbtn pri" id="bdNeedLogin" type="button" style="margin-top:8px">去登录新版</button></div>`;
          const btn = sBox.querySelector('#bdNeedLogin');
          if (btn) btn.onclick = () => { closeModal(); if (optsCfg.promptLogin) optsCfg.promptLogin('y2'); };
          return;
        }
        const url = '/api/y2/sessions?courseId=' + encodeURIComponent(course.id)
          + '&termName=' + encodeURIComponent(course.term || '');
        const j = await fetch(url).then(r => r.json());
        if (!j.ok) {
          sBox.innerHTML = `<div class="bdempty">读取失败：${esc(j.error || '未知错误')}</div>`;
          return;
        }
        sessions = (j.sessions || []).map((s, idx) => mapY2Session(s, idx));
      } catch (e) {
        sBox.innerHTML = `<div class="bdempty">请求失败：${esc(e.message)}</div>`;
        return;
      }
    } else {
      try {
        const st = await fetch('/api/status').then(r => r.json());
        if (!st || !st.loggedIn) {
          sBox.innerHTML = `<div class="bdempty">需要登录老平台后才能读取讲次列表。<br><button class="bdbtn pri" id="bdNeedLogin" type="button" style="margin-top:8px">去登录老平台</button></div>`;
          const btn = sBox.querySelector('#bdNeedLogin');
          if (btn) btn.onclick = () => { closeModal(); if (optsCfg.promptLogin) optsCfg.promptLogin('old'); };
          return;
        }
        const j = await fetch('/api/open?id=' + encodeURIComponent(course.id)).then(r => r.json());
        if (!j.ok) {
          sBox.innerHTML = `<div class="bdempty">读取失败：${esc(j.error || '未知错误')}</div>`;
          return;
        }
        sessions = (j.sessions || []).map((s, idx) => mapOldSession(s, idx));
      } catch (e) {
        sBox.innerHTML = `<div class="bdempty">请求失败：${esc(e.message)}</div>`;
        return;
      }
    }

    renderSessions();
  }

  function renderSessions() {
    const sBox = modal.querySelector('#bdSessionList');
    sBox.innerHTML = '';
    const playableCount = sessions.filter(s => s.playable).length;
    modal.querySelector('#bdSelAll').disabled = playableCount === 0;
    modal.querySelector('#bdSelNone').disabled = playableCount === 0;
    modal.querySelector('#bdCourseMeta').textContent = [
      activeCourse && activeCourse.teacher,
      activeCourse && activeCourse.term,
      playableCount + ' 讲可下载 / 共 ' + sessions.length + ' 讲',
    ].filter(Boolean).join(' · ');

    if (!sessions.length) {
      sBox.innerHTML = '<div class="bdempty">这门课暂无可下载的讲次记录。</div>';
      updateCounts();
      return;
    }

    for (const s of sessions) {
      const row = document.createElement('label');
      row.className = 'bdsess' + (s.playable ? '' : ' dead');
      row.innerHTML = `<input type="checkbox" ${s.checked ? 'checked' : ''} ${s.playable ? '' : 'disabled'}>`
        + `<div class="info"><div class="st">${esc(s.title)}</div><div class="sm">${esc(s.subText)}</div></div>`;
      const cb = row.querySelector('input');
      cb.onchange = () => {
        s.checked = cb.checked;
        updateCounts();
      };
      sBox.appendChild(row);
    }
    updateCounts();
  }

  function toggleAll(flag) {
    for (const s of sessions) {
      if (s.playable) s.checked = !!flag;
    }
    modal.querySelectorAll('#bdSessionList input[type=checkbox]').forEach((cb, i) => {
      if (!cb.disabled && sessions[i]) cb.checked = sessions[i].checked;
    });
    updateCounts();
  }

  function buildTasksForSelection(probed) {
    if (!activeCourse) return [];
    const vm = modal.querySelector('#bdViewMode').value;
    const tasks = [];
    const picked = sessions.filter(s => s.playable && s.checked);
    for (const s of picked) {
      if (activeCourse.platform === 'y2') {
        let wants = [vm];
        if (vm === 'both') {
          if (probed) {
            wants = [];
            if (s.hasMain !== false) wants.push('camera');
            if (s.hasVga) wants.push('screen');
            if (!wants.length) wants.push('camera');
          } else {
            wants = ['camera', 'screen'];
          }
        } else if (vm === 'dual') {
          wants = s.hasVga === false ? ['camera'] : ['dual'];
        }
        for (const k of wants) {
          const label = k === 'dual' ? '双画面' : k === 'screen' ? '课件' : '教师画面';
          const title = (activeCourse.title || '课程') + '_' + (s.date ? s.date.slice(0, 10) : ('讲' + (s.idx + 1))) + '_' + label;
          const url = '/y2dl?c=' + encodeURIComponent(activeCourse.id)
            + '&s=' + encodeURIComponent(s.id)
            + '&k=' + encodeURIComponent(k)
            + '&title=' + encodeURIComponent(title);
          const displayTitle = !probed
            ? (title + (k === 'screen' ? ' (课件)' : k === 'dual' ? ' (双画面)' : ''))
            : title;
          tasks.push({ url, title: displayTitle });
        }
      } else {
        const wants = [];
        if (vm === 'dual') {
          if (s.hasMain && s.hasVga) wants.push('dual');
          else if (s.hasMain) wants.push('main');
          else if (s.hasVga) wants.push('vga');
        } else if (vm === 'both') {
          if (s.hasMain) wants.push('main');
          if (s.hasVga) wants.push('vga');
        } else if (vm === 'screen') {
          if (s.hasVga) wants.push('vga');
          else if (s.hasMain) wants.push('main');
        } else {
          if (s.hasMain) wants.push('main');
          else if (s.hasVga) wants.push('vga');
        }
        for (const k of wants) {
          const label = k === 'dual' ? '双画面' : k === 'vga' ? '课件' : '全景';
          const title = (activeCourse.title || '课程') + '_' + (s.date ? s.date.slice(0, 10) : ('讲' + (s.idx + 1))) + '_' + label;
          const url = '/dl?course=' + encodeURIComponent(activeCourse.id)
            + '&session=' + encodeURIComponent(s.id)
            + '&kind=' + k
            + '&title=' + encodeURIComponent(title);
          tasks.push({ url, title });
        }
      }
    }
    return tasks;
  }

  function updateCounts() {
    if (!modal) return;
    const checkedN = sessions.filter(s => s.playable && s.checked).length;
    const tasks = buildTasksForSelection();
    modal.querySelector('#bdCountTxt').textContent = `已选 ${checkedN} 讲（${tasks.length} 个文件）`;
    modal.querySelector('#bdStartBtn').disabled = tasks.length === 0;
    modal.querySelector('#bdStartBtn').textContent = tasks.length > 0 ? `⤓ 批量开始下载（${tasks.length}）` : '⤓ 批量开始下载';
  }

  async function pumpBrowserQueue() {
    if (browserPumping) return;
    browserPumping = true;
    try {
      while (browserQueue.length > 0) {
        let running = 0;
        try {
          const j = await fetch('/api/downloads').then(r => r.json());
          running = ((j && j.items) || []).filter(x => x.status === 'running').length;
        } catch (e) {}
        if (running >= 2) {
          await new Promise(r => setTimeout(r, 1500));
          continue;
        }
        const task = browserQueue.shift();
        if (task) {
          window.open(task.url, '_blank');
          await new Promise(r => setTimeout(r, 900));
        }
      }
    } finally {
      browserPumping = false;
    }
  }

  async function startBatchDownload() {
    if (!activeCourse) return;
    const vm = modal.querySelector('#bdViewMode').value;
    const picked = sessions.filter(s => s.playable && s.checked);
    if (!picked.length) {
      toastMsg('请先勾选至少 1 讲');
      return;
    }

    // 新版选择「双画面 / 全景+课件 / 仅课件」时，快速并发探一次实际存在的流，避免无课件讲次重复下或误选双画面
    if (activeCourse.platform === 'y2' && (vm === 'both' || vm === 'screen' || vm === 'dual')) {
      const btn = modal.querySelector('#bdStartBtn');
      const oldTxt = btn.textContent;
      btn.disabled = true;
      btn.textContent = '正在核对可用画面…';
      let cursor = 0;
      const worker = async () => {
        while (cursor < picked.length) {
          const s = picked[cursor++];
          try {
            const j = await fetch(`/api/y2/sub?courseId=${encodeURIComponent(activeCourse.id)}&subId=${encodeURIComponent(s.id)}`).then(r => r.json());
            if (j && j.ok && j.info && Array.isArray(j.info.streams)) {
              const types = new Set(j.info.streams.map(x => x.type));
              s.hasMain = types.has('camera') || types.has('room');
              s.hasVga = types.has('screen');
            }
          } catch (e) {}
        }
      };
      await Promise.all(Array.from({ length: Math.min(6, picked.length) }, worker));
      btn.disabled = false;
      btn.textContent = oldTxt;
    }

    const tasks = buildTasksForSelection(true);

    if (!tasks.length) {
      toastMsg('所选讲次暂无对应画面');
      return;
    }
    if (window.yhktApp && window.yhktApp.downloadBatch) {
      window.yhktApp.downloadBatch(tasks);
      closeModal();
      if (optsCfg.openDownloads) setTimeout(() => optsCfg.openDownloads(), 350);
    } else {
      browserQueue.push(...tasks);
      toastMsg(`已加入 ${tasks.length} 个下载任务（自动双路排队防限流），可在「下载管理」查看进度`, 4200);
      closeModal();
      pumpBrowserQueue();
      if (optsCfg.openDownloads) setTimeout(() => optsCfg.openDownloads(), 600);
    }
  }

  async function syncFFmpegMode() {
    if (!modal) return;
    let hasFF = window.__yhktHasFFmpeg;
    if (typeof hasFF !== 'boolean') {
      try {
        const st = await (await fetch('/api/status')).json();
        hasFF = !!(st && st.hasFFmpeg);
        window.__yhktHasFFmpeg = hasFF;
      } catch (e) { hasFF = false; }
    }
    const sel = modal.querySelector('#bdViewMode');
    if (!sel) return;
    const optDual = sel.querySelector('option[data-ffmpeg-only="1"]');
    if (optDual) {
      optDual.hidden = !hasFF;
      optDual.disabled = !hasFF;
    }
    if (!hasFF && sel.value === 'dual') {
      sel.value = 'both';
      updateCounts();
    }
  }

  async function openModal(preCourse) {
    ensureUI();
    await Promise.all([loadTerms(), refreshSaveHint(), syncFFmpegMode()]);
    const platEl = modal.querySelector('#bdPlat');
    if (platEl && optsCfg.defaultPlatform) platEl.value = optsCfg.defaultPlatform;
    modal.style.display = 'flex';
    if (preCourse && preCourse.id) {
      const box = modal.querySelector('#bdCourseList');
      if (box && (!box.querySelector('.bdrow') || box.querySelector('.bdempty') || box.dataset.fromCur === '1')) {
        box.dataset.fromCur = '1';
        box.innerHTML = '';
        const row = document.createElement('div');
        row.className = 'bdrow on';
        row.innerHTML = `<div class="t"><span class="bdtag ${preCourse.platform}">${preCourse.platform === 'y2' ? '新版' : '老平台'}</span><span>${esc(preCourse.title)}</span><span class="bdtag">当前课程</span></div>`
          + `<div class="m">${preCourse.term ? `<span>${esc(preCourse.term)}</span>` : ''}${preCourse.teacher ? `<span>${esc(preCourse.teacher)}</span>` : ''}</div>`;
        row.onclick = () => {
          box.querySelectorAll('.bdrow').forEach(x => x.classList.remove('on'));
          row.classList.add('on');
          loadCourseSessions(preCourse);
        };
        box.appendChild(row);
      }
      loadCourseSessions(preCourse);
    } else {
      const inp = modal.querySelector('#bdSearchInp');
      if (inp && !inp.value.trim() && optsCfg.defaultQuery) {
        inp.value = optsCfg.defaultQuery();
        if (inp.value.trim()) doSearch();
      }
      if (inp) setTimeout(() => inp.focus(), 60);
    }
  }

  function closeModal() {
    if (modal) modal.style.display = 'none';
  }

  window.yhktBatch = {
    open: openModal,
    close: closeModal,
    isOpen: () => !!(modal && modal.style.display === 'flex'),
    init(opts) {
      optsCfg = opts || {};
      const btn = document.getElementById('btnBatchDl');
      if (btn) btn.onclick = e => {
        e.preventDefault();
        if (optsCfg.onBeforeOpen) optsCfg.onBeforeOpen();
        const cur = typeof optsCfg.getCurrentCourse === 'function' ? optsCfg.getCurrentCourse() : null;
        openModal(cur);
      };
    },
  };
})();
