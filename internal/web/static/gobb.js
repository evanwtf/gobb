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
