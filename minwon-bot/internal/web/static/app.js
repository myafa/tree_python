(function () {
  'use strict';
  var csrf = (document.querySelector('meta[name="csrf"]') || {}).content || '';

  function toast(msg, isErr) {
    var t = document.getElementById('toast');
    if (!t) return;
    t.textContent = msg;
    t.className = 'show' + (isErr ? ' err' : '');
    clearTimeout(toast.timer);
    toast.timer = setTimeout(function () { t.className = ''; }, isErr ? 9000 : 3000);
  }

  function post(url, body) {
    return fetch(url, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrf },
      body: JSON.stringify(body || {})
    }).then(function (r) {
      return r.json().catch(function () { return {}; }).then(function (j) {
        if (!r.ok) throw new Error(j.error || ('요청 실패 (' + r.status + ')'));
        return j;
      });
    });
  }

  function val(id) { var e = document.getElementById(id); return e ? e.value : ''; }
  function lines(s) { return s.split('\n').map(function (x) { return x.trim(); }).filter(Boolean); }

  // AI 호출은 오래 걸릴 수 있어 버튼을 잠그고 안내를 바꾼다.
  function busy(btn, text, fn) {
    var old = btn.textContent;
    btn.disabled = true;
    btn.textContent = text;
    return fn().catch(function (e) { toast(e.message, true); })
      .then(function () { btn.disabled = false; btn.textContent = old; });
  }

  function renderFindings(list, findings) {
    if (!list) return;
    list.textContent = '';
    (findings || []).forEach(function (f) {
      var li = document.createElement('li');
      li.className = f.severity;
      li.textContent = f.message;
      list.appendChild(li);
    });
  }

  function chk(id) { var e = document.getElementById(id); return !!(e && e.checked); }
  function int(id, dflt) { var n = parseInt(val(id), 10); return isNaN(n) ? dflt : n; }

  // 설정 화면의 모든 입력을 한 번에 모은다. 비밀 값(토큰, 비밀번호)은 비워 두면 서버가 저장된 값을 유지한다.
  function collectSettings() {
    return {
      llm: {
        backend: val('s-backend'), model: val('s-model').trim(), effort: val('s-effort'),
        claude_bin: val('s-bin').trim(), timeout_sec: int('s-timeout', 0)
      },
      limits: {
        daily_max: int('s-daily', 0), min_gap_minutes: int('s-gap', 0),
        repeat_round: int('s-repeat', 0), similarity: parseFloat(val('s-sim')) || 0
      },
      auto: {
        enabled: chk('s-auto-enabled'), answer_auto: chk('s-auto-answer'),
        check_interval_hours: int('s-auto-interval', 0), hold_policy: val('s-auto-hold'),
        daily_summary_hour: int('s-auto-daily', -1)
      },
      report: {
        detail: val('s-rep-detail'),
        telegram: { token: val('s-tg-token').trim(), chat_id: val('s-tg-chat').trim() },
        email: {
          host: val('s-em-host').trim(), port: int('s-em-port', 0), tls: val('s-em-tls'),
          username: val('s-em-user').trim(), password: val('s-em-pass'),
          from: val('s-em-from').trim(), to: val('s-em-to').trim()
        }
      },
      mask_terms: lines(val('s-mask'))
    };
  }

  var AI_WAIT = 'AI 가 작업 중… (최대 몇 분)';

  var actions = {
    draft: function (btn) {
      var caseId = btn.dataset.case;
      var extra = [];
      document.querySelectorAll('.qa-input').forEach(function (e) {
        extra.push({ question: e.dataset.q, answer: e.value });
      });
      return busy(btn, AI_WAIT, function () {
        return post('/api/cases/' + caseId + '/draft', { extra: extra }).then(function (j) {
          if (!j.needs_info) { location.reload(); return; }
          var box = document.getElementById('qa-box');
          box.textContent = '';
          var p = document.createElement('p');
          p.textContent = 'AI 가 정확한 초안을 쓰려면 아래 내용이 더 필요하다고 합니다. 답을 적고 다시 "초안 작성"을 누르세요.';
          box.appendChild(p);
          j.questions.forEach(function (q) {
            var l = document.createElement('label');
            l.textContent = q;
            var t = document.createElement('textarea');
            t.className = 'qa-input'; t.rows = 2; t.dataset.q = q;
            box.appendChild(l); box.appendChild(t);
          });
        });
      });
    },
    save: function (btn) {
      var id = btn.dataset.sub;
      return busy(btn, '저장 중…', function () {
        return post('/api/submissions/' + id + '/save', {
          title: val('title-' + id), body: val('body-' + id), asks: lines(val('asks-' + id))
        }).then(function (j) {
          renderFindings(document.getElementById('findings-' + id), j.findings);
          toast('저장했습니다.');
        });
      });
    },
    copy: function (btn) {
      var el = document.getElementById(btn.dataset.target);
      return navigator.clipboard.writeText(el.value).then(function () { toast('복사했습니다.'); },
        function () { el.select(); toast('자동 복사가 막혀 있습니다. 선택된 글을 직접 복사하세요 (Ctrl+C).', true); });
    },
    submitted: function (btn) {
      var id = btn.dataset.sub;
      var no = val('receipt-' + id).trim();
      if (!no) { toast('접수번호를 입력하세요.', true); return; }
      return busy(btn, '기록 중…', function () {
        return post('/api/submissions/' + id + '/submitted', {
          receipt_no: no, title: val('title-' + id), body: val('body-' + id), asks: lines(val('asks-' + id))
        }).then(function () { location.reload(); });
      });
    },
    discard: function (btn) {
      if (!confirm('이 초안을 버립니다. 계속할까요?')) return;
      return busy(btn, '…', function () {
        return post('/api/submissions/' + btn.dataset.sub + '/discard').then(function () { location.reload(); });
      });
    },
    'answer-add': function (btn) {
      var id = btn.dataset.sub;
      var body = val('answer-body-' + id);
      if (!body.trim()) { toast('답변 본문을 붙여넣으세요.', true); return; }
      return busy(btn, '저장 중…', function () {
        return post('/api/submissions/' + id + '/answers', { body: body, dept: val('answer-dept-' + id) })
          .then(function (j) {
            if (j.auto) toast('답변을 저장했습니다. 승인 없이 자동으로 평가하고 처리합니다. 결과는 이 화면과 보고함에 나타납니다.');
            setTimeout(function () { location.reload(); }, j.auto ? 1200 : 0);
          });
      });
    },
    evaluate: function (btn) {
      return busy(btn, AI_WAIT, function () {
        return post('/api/answers/' + btn.dataset.answer + '/evaluate').then(function () { location.reload(); });
      });
    },
    followup: function (btn) {
      return busy(btn, AI_WAIT, function () {
        return post('/api/answers/' + btn.dataset.answer + '/followup').then(function () { location.reload(); });
      });
    },
    'close-case': function (btn) {
      var reason = val('close-reason').trim();
      if (!reason) { toast('종결 사유를 적으세요.', true); return; }
      return busy(btn, '…', function () {
        return post('/api/cases/' + btn.dataset.case + '/close', { reason: reason }).then(function () { location.reload(); });
      });
    },
    'save-settings': function (btn) {
      return busy(btn, '저장 중…', function () {
        return post('/api/settings', collectSettings()).then(function () {
          toast('설정을 저장했습니다.');
          setTimeout(function () { location.reload(); }, 600);
        });
      });
    },
    'report-test': function (btn) {
      var ch = btn.dataset.channel;
      return busy(btn, '보내는 중…', function () {
        // 입력한 값으로 시험하도록 먼저 저장한다.
        return post('/api/settings', collectSettings())
          .then(function () { return post('/api/report/test', { channel: ch }); })
          .then(function (j) { toast(j.channel + '로 시험 메시지를 보냈습니다.'); });
      });
    },
    'report-clear': function (btn) {
      if (!confirm('텔레그램·이메일 설정(토큰, 비밀번호 포함)을 지웁니다. 계속할까요?')) return;
      return busy(btn, '…', function () {
        return post('/api/report/clear').then(function () { location.reload(); });
      });
    },
    'auto-run': function (btn) {
      return busy(btn, '확인 중…', function () {
        return post('/api/auto/run').then(function () { toast('확인을 시작했습니다. 결과는 보고함에 쌓입니다.'); });
      });
    },
    'case-auto': function (btn) {
      return busy(btn, '…', function () {
        return post('/api/cases/' + btn.dataset.case + '/auto', { off: btn.dataset.off === '1' })
          .then(function () { location.reload(); });
      });
    },
    'reopen-case': function (btn) {
      return busy(btn, '…', function () {
        return post('/api/cases/' + btn.dataset.case + '/reopen').then(function () { location.reload(); });
      });
    },
    'clear-attention': function (btn) {
      return busy(btn, '…', function () {
        return post('/api/cases/' + btn.dataset.case + '/attention/clear').then(function () { location.reload(); });
      });
    },
    'llm-test': function (btn) {
      return busy(btn, '확인 중…', function () {
        return post('/api/llm/test').then(function (j) { toast('연결 정상: ' + j.name + ' (' + j.ms + 'ms)'); });
      });
    },
    'style-add': function (btn) {
      var text = val('style-text');
      if (!text.trim()) { toast('글을 붙여넣으세요.', true); return; }
      return busy(btn, '…', function () {
        return post('/api/style/samples', { text: text }).then(function () { location.reload(); });
      });
    },
    'style-delete': function (btn) {
      return busy(btn, '…', function () {
        return post('/api/style/samples/' + btn.dataset.id + '/delete').then(function () { location.reload(); });
      });
    },
    'style-extract': function (btn) {
      return busy(btn, AI_WAIT, function () {
        return post('/api/style/extract').then(function () { location.reload(); });
      });
    },
    'style-reset': function (btn) {
      if (!confirm('학습된 문체 프로필을 지웁니다. 붙여넣은 글은 남습니다.')) return;
      return busy(btn, '…', function () {
        return post('/api/style/reset').then(function () { location.reload(); });
      });
    }
  };

  document.addEventListener('click', function (ev) {
    var btn = ev.target.closest('[data-action]');
    if (!btn || btn.disabled) return;
    var fn = actions[btn.dataset.action];
    if (!fn) return;
    ev.preventDefault();
    var r = fn(btn);
    if (r && r.catch) r.catch(function (e) { toast(e.message, true); });
  });

  // 자동 처리 결과를 기다리는 동안(방금 기록한 답변)에는 주기적으로 새로 고친다. 서버가 기다림이 끝나면 표시를 없앤다.
  if (document.querySelector('[data-polling="1"]')) {
    setTimeout(function () { location.reload(); }, 8000);
  }

  // 설정 화면: 방식을 바꾸면 모델 입력란의 기본값을 맞춘다.
  var backend = document.getElementById('s-backend');
  if (backend) {
    backend.addEventListener('change', function () {
      var m = document.getElementById('s-model');
      if (backend.value === 'api' && !/^claude-/.test(m.value)) m.value = 'claude-opus-5-5';
      if (backend.value === 'claude_code' && /^claude-/.test(m.value)) m.value = 'opus';
    });
  }
})();
