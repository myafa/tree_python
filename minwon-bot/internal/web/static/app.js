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
          .then(function () { location.reload(); });
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
      var cfg = {
        llm: {
          backend: val('s-backend'), model: val('s-model').trim(), effort: val('s-effort'),
          claude_bin: val('s-bin').trim(), timeout_sec: parseInt(val('s-timeout'), 10) || 0
        },
        limits: {
          daily_max: parseInt(val('s-daily'), 10) || 0, min_gap_minutes: parseInt(val('s-gap'), 10) || 0,
          repeat_round: parseInt(val('s-repeat'), 10) || 0, similarity: parseFloat(val('s-sim')) || 0
        },
        mask_terms: lines(val('s-mask'))
      };
      return busy(btn, '저장 중…', function () {
        return post('/api/settings', cfg).then(function () { toast('설정을 저장했습니다.'); setTimeout(function () { location.reload(); }, 600); });
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
