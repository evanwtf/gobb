// gobb.js: the small amount of script that the pages use. Every page works
// without it. A spoiler also opens while it has focus (see gobb.css).
(function () {
  "use strict";

  // With script, a click and Enter control a spoiler. Without it, focus does.
  document.documentElement.classList.add("js");

  // A spoiler opens on a click and on Enter, and closes the same way.
  function toggle(el) {
    el.classList.toggle("shown");
  }

  document.addEventListener("click", function (event) {
    var target = event.target;
    if (!(target instanceof Element)) {
      return;
    }
    var spoiler = target.closest(".spoiler");
    // A click on a link inside a spoiler follows the link.
    if (spoiler && !target.closest("a")) {
      toggle(spoiler);
    }
  });

  document.addEventListener("keydown", function (event) {
    var target = event.target;
    if (event.key !== "Enter" || !(target instanceof Element)) {
      return;
    }
    if (target.classList.contains("spoiler")) {
      event.preventDefault();
      toggle(target);
    }
  });
})();

// ---- Auto-save of the account page -------------------------------------------
// A form with data-autosave saves each field when its change event fires. The
// field goes to the form's own address with fetch, plus the csrf token of the
// form, and the header X-Gobb-Save: 1, which asks for 204 or JSON as the answer.
// The status line of the field (id "{name}-status") says "Saving…", then "Saved.",
// or the message of the server. Every piece of text is written with textContent,
// and no markup is built from a string. Without this script, the Save buttons of
// the forms work: the server answers a form post with a redirect.
(function () {
  "use strict";

  var forms = document.querySelectorAll("form[data-autosave]");
  if (forms.length === 0 || !window.fetch || !window.FormData || !window.URLSearchParams) {
    return;
  }

  var MAX_FILE = 1024 * 1024;
  // A keepalive body may be at most 64 KiB (the Fetch standard), so a larger text
  // goes without it.
  var MAX_KEEPALIVE = 60000;

  // state holds, for each unit of saving, whether a save is on its way and whether
  // a later change waits behind it. lastSent holds the value of each text box as
  // it was loaded or last sent, so that leaving the page can tell a changed box.
  var state = new WeakMap();
  var lastSent = new WeakMap();

  function isText(el) {
    return el instanceof HTMLTextAreaElement ||
      (el instanceof HTMLInputElement && ["text", "search", "url", "email"].indexOf(el.type) >= 0);
  }

  // unitOf returns what one save covers: the birthday's three lists, the date
  // format's three choices, or one field.
  function unitOf(el) {
    var birthday = el.closest("[data-birthday]");
    if (birthday) {
      return birthday;
    }
    if (el instanceof HTMLInputElement && el.type === "radio") {
      return el.closest("fieldset") || el;
    }
    return el;
  }

  function unitName(unit) {
    if (unit.hasAttribute("data-birthday")) {
      return "birthday";
    }
    if (unit.tagName === "FIELDSET") {
      var first = unit.querySelector("input[name]");
      return first ? first.name : "";
    }
    if (unit.id === "avatar-remove") {
      return "avatar";
    }
    return unit.name || "";
  }

  function controlsOf(unit) {
    if (unit.hasAttribute("data-birthday")) {
      return unit.querySelectorAll("select");
    }
    if (unit instanceof HTMLFormElement || unit.tagName === "FIELDSET") {
      return [];
    }
    return [unit];
  }

  // show writes the status of a unit. A refused unit keeps its value and gets
  // aria-invalid.
  function show(unit, text, bad) {
    var line = document.getElementById(unitName(unit) + "-status");
    if (line) {
      line.textContent = text;
      line.classList.toggle("field-error", bad);
    }
    Array.prototype.forEach.call(controlsOf(unit), function (c) {
      if (bad) {
        c.setAttribute("aria-invalid", "true");
      } else {
        c.removeAttribute("aria-invalid");
      }
    });
  }

  function tokenOf(form) {
    var input = form.querySelector('input[name="csrf"]');
    return input ? input.value : "";
  }

  // build returns what to send for a unit: {body, text} for a request, {skip} for
  // nothing to send, or {message, bad} for a problem that the page can name
  // before it asks the server.
  function build(unit, form) {
    var params = new URLSearchParams();
    params.append("csrf", tokenOf(form));
    if (unit.hasAttribute("data-birthday")) {
      var day = unit.querySelector('[name="bday"]').value;
      var month = unit.querySelector('[name="bmonth"]').value;
      var year = unit.querySelector('[name="byear"]').value;
      // The birthday saves with a day and a month, or with neither.
      if ((day === "") !== (month === "")) {
        return {message: "Not saved yet: pick both a day and a month, or neither.", bad: false};
      }
      if (day === "" && year !== "") {
        return {message: "Not saved yet: a year needs a day and a month.", bad: false};
      }
      params.append("bday", day);
      params.append("bmonth", month);
      params.append("byear", year);
    } else if (unit.tagName === "FIELDSET") {
      var chosen = unit.querySelector("input:checked");
      if (!chosen) {
        return {skip: true};
      }
      params.append(chosen.name, chosen.value);
    } else if (unit.type === "file") {
      var file = unit.files[0];
      if (!file) {
        return {skip: true};
      }
      if (file.size > MAX_FILE) {
        return {message: "Not saved: that file is over 1 MB.", bad: true};
      }
      var data = new FormData();
      data.append("csrf", tokenOf(form));
      data.append("avatar", file);
      return {body: data, file: true};
    } else {
      params.append(unit.name, unit.value);
    }
    return {body: params.toString(), text: true};
  }

  // post sends a body and resolves to {ok, data}: ok for 204 and 200, with the
  // JSON of a 200 or of a refusal in data. A network failure rejects.
  function post(url, req) {
    var init = {
      method: "POST",
      credentials: "same-origin",
      headers: {"X-Gobb-Save": "1"},
      body: req.body
    };
    if (req.text) {
      init.headers["Content-Type"] = "application/x-www-form-urlencoded;charset=UTF-8";
      // A text goes with keepalive, so that a save started as the page closes can
      // finish. A file does not: its body can pass the 64 KiB cap.
      init.keepalive = req.body.length < MAX_KEEPALIVE;
    }
    return fetch(url, init).then(function (r) {
      if (r.status === 204) {
        return {ok: true, data: {}};
      }
      return r.json().then(function (data) {
        return {ok: r.status === 200, data: data || {}};
      }, function () {
        return {ok: false, data: {}};
      });
    });
  }

  // nowIn writes the time now in a zone, in the standard format of the server.
  function nowIn(zone) {
    var text = new Intl.DateTimeFormat("en-US", {
      month: "short", day: "numeric", year: "numeric",
      hour: "numeric", minute: "2-digit", timeZone: zone
    }).format(new Date());
    return text.replace(/[\s ](AM|PM)$/, function (m, half) {
      return " " + half.toLowerCase();
    });
  }

  function showAvatar(src) {
    var holder = document.getElementById("avatar-show");
    var remove = document.getElementById("avatar-remove");
    if (!holder || typeof src !== "string") {
      return;
    }
    var img = holder.querySelector("img");
    if (!img) {
      img = document.createElement("img");
      img.className = "avatar-img";
      img.alt = "Your avatar";
      img.width = 96;
      img.height = 96;
      holder.replaceChildren(img);
    }
    img.src = src;
    if (remove) {
      remove.hidden = false;
    }
  }

  function hideAvatar() {
    var holder = document.getElementById("avatar-show");
    var remove = document.getElementById("avatar-remove");
    if (holder) {
      var initial = document.createElement("span");
      initial.className = "avatar avatar-lg avatar-none";
      initial.setAttribute("aria-hidden", "true");
      initial.textContent = holder.getAttribute("data-initial") || "?";
      holder.replaceChildren(initial);
    }
    if (remove) {
      remove.hidden = true;
    }
  }

  // saved runs after the server accepted a unit.
  function saved(unit, data) {
    if (unit.name === "timezone") {
      var line = document.getElementById("footer-zone");
      if (line) {
        line.textContent = "Times are shown in " + unit.value + ".";
      }
      var now = document.getElementById("timezone-now");
      var city = document.getElementById("timezone-city");
      if (now) {
        try {
          now.textContent = nowIn(unit.value);
          if (city) {
            city.textContent = unit.value.slice(unit.value.lastIndexOf("/") + 1).replace(/_/g, " ");
          }
        } catch (e) {
          // A zone that this browser does not know: the hint keeps its text.
        }
      }
    } else if (unit.type === "file") {
      showAvatar(data.src);
    }
  }

  function stateOf(unit) {
    var st = state.get(unit);
    if (!st) {
      st = {busy: false, again: false};
      state.set(unit, st);
    }
    return st;
  }

  // finish runs when a save ends. A change that came in meanwhile goes next, with
  // its latest value only.
  function finish(unit, res) {
    var st = stateOf(unit);
    st.busy = false;
    if (res.ok) {
      saved(unit, res.data);
      show(unit, "Saved.", false);
    } else {
      show(unit, res.data.message || "Not saved. Try again in a moment.", true);
    }
    if (unit.type === "file" && !st.again) {
      // Clear the pick, so that the same file can be picked again.
      unit.value = "";
    }
    if (st.again) {
      st.again = false;
      save(unit, false);
    }
  }

  // save sends a unit. Only one save of a unit is on its way at a time. When the
  // page is closing, the save goes at once.
  function save(unit, leaving) {
    var st = stateOf(unit);
    if (st.busy && !leaving) {
      st.again = true;
      return;
    }
    var form = unit.closest("form");
    var req = build(unit, form);
    if (req.skip) {
      return;
    }
    if (req.message) {
      show(unit, req.message, req.bad);
      return;
    }
    st.busy = true;
    if (isText(unit)) {
      lastSent.set(unit, unit.value);
    }
    show(unit, "Saving…", false);
    post(form.action, req).then(function (res) {
      finish(unit, res);
    }, function () {
      finish(unit, {ok: false, data: {message: "Not saved: the server could not be reached. Try again."}});
    });
  }

  Array.prototype.forEach.call(forms, function (form) {
    Array.prototype.forEach.call(form.querySelectorAll("input, textarea"), function (el) {
      if (isText(el)) {
        lastSent.set(el, el.value);
      }
    });
    form.addEventListener("change", function (event) {
      var el = event.target;
      if (el instanceof HTMLElement && (el.name || el.type === "file") && el.name !== "csrf") {
        save(unitOf(el), false);
      }
    });
    // Enter in a one-line box saves that box. It does not send the whole form.
    form.addEventListener("submit", function (event) {
      event.preventDefault();
      var el = document.activeElement;
      if (el && form.contains(el) && isText(el) && el.value !== lastSent.get(el)) {
        save(unitOf(el), false);
      }
    });
  });

  // Remove avatar goes through the same single file of saves as an upload.
  var remove = document.getElementById("avatar-remove");
  var picker = document.getElementById("avatar");
  if (remove && picker) {
    remove.addEventListener("submit", function (event) {
      event.preventDefault();
      var st = stateOf(picker);
      if (st.busy) {
        show(remove, "Wait until the upload ends.", true);
        return;
      }
      st.busy = true;
      show(remove, "Saving…", false);
      var body = new URLSearchParams();
      body.append("csrf", tokenOf(remove));
      // An upload that was picked while the remove ran goes next, with its latest
      // file, as after any other save.
      function next() {
        st.busy = false;
        if (st.again) {
          st.again = false;
          save(picker, false);
        }
      }
      post(remove.action, {body: body.toString(), text: true}).then(function (res) {
        if (res.ok) {
          hideAvatar();
          show(remove, "Removed.", false);
        } else {
          show(remove, res.data.message || "Not saved. Try again in a moment.", true);
        }
        next();
      }, function () {
        show(remove, "Not saved: the server could not be reached. Try again.", true);
        next();
      });
    });
  }

  // On leaving the page, a text box that still has focus and holds a change that
  // has not gone out is sent now: the browser fires no change event for it.
  window.addEventListener("pagehide", function () {
    var el = document.activeElement;
    if (el && isText(el) && el.form && el.form.hasAttribute("data-autosave") && el.value !== lastSent.get(el)) {
      save(unitOf(el), true);
    }
  });
})();
