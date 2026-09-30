// Scroll to the first search-highlighted match after MkDocs Material navigates.
// Works with navigation.instant (which skips DOMContentLoaded on page transitions).
//
// MkDocs Material exposes a global RxJS observable `document$` that emits on
// every page load, including instant-navigation transitions.

function scrollToHighlight() {
  var params = new URLSearchParams(window.location.search);
  if (!params.has("h")) return;

  // MkDocs Material injects <mark data-md-highlight> asynchronously after
  // the page content is swapped in.  Poll briefly until marks appear.
  var attempts = 0;
  var interval = setInterval(function () {
    var mark = document.querySelector("mark[data-md-highlight]");
    if (mark) {
      clearInterval(interval);
      mark.scrollIntoView({ behavior: "smooth", block: "center" });
    }
    if (++attempts > 20) clearInterval(interval);   // give up after ~2s
  }, 100);
}

// Instant navigation: fires on every page swap
if (typeof document$ !== "undefined") {
  document$.subscribe(function () { scrollToHighlight(); });
} else {
  // Fallback for first load / non-instant mode
  document.addEventListener("DOMContentLoaded", scrollToHighlight);
}
