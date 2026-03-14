// Shared sidebar navigation — single source of truth for all doc pages.
// Each page includes <nav class="sidebar" id="sidebar"></nav> and loads this script.

(function () {
  // Sidebar link definitions. Hrefs are always relative to the page that "owns"
  // the anchor (e.g. "workflow.html#states"). The render step rewrites links
  // that target the current page to bare "#anchor" hrefs so scroll-spy works.
  var sections = [
    {
      title: "Getting started",
      links: [
        { href: "index.html#overview", text: "Overview", type: "link" },
        { href: "index.html#how-it-works", text: "How it works", type: "link" },
        { href: "index.html#install", text: "Installation", type: "link" },
        { href: "index.html#quickstart", text: "Quick start", type: "sub" },
      ],
    },
    {
      title: "Workflow",
      links: [
        { href: "workflow.html#workflow", text: "Configuration", type: "link" },
        { href: "workflow.html#settings", text: "settings", type: "sub" },
        { href: "workflow.html#source-filter", text: "source.filter", type: "sub" },
        { href: "workflow.html#states", text: "State types", type: "link" },
        { href: "workflow.html#state-task", text: "task", type: "sub" },
        { href: "workflow.html#state-wait", text: "wait", type: "sub" },
        { href: "workflow.html#state-choice", text: "choice", type: "sub" },
        { href: "workflow.html#state-pass", text: "pass", type: "sub" },
        { href: "workflow.html#state-template", text: "template", type: "sub" },
        { href: "workflow.html#templates", text: "Built-in templates", type: "link" },
        { href: "workflow.html#tmpl-plan", text: "builtin:plan", type: "sub" },
        { href: "workflow.html#tmpl-code", text: "builtin:code", type: "sub" },
        { href: "workflow.html#tmpl-pr", text: "builtin:pr", type: "sub" },
        { href: "workflow.html#tmpl-ci", text: "builtin:ci", type: "sub" },
        { href: "workflow.html#tmpl-review", text: "builtin:review", type: "sub" },
        { href: "workflow.html#tmpl-merge", text: "builtin:merge", type: "sub" },
        { href: "workflow.html#tmpl-asana-move", text: "builtin:asana_move_section", type: "sub" },
        { href: "workflow.html#tmpl-linear-move", text: "builtin:linear_move_state", type: "sub" },
        { href: "workflow.html#tmpl-asana-await", text: "builtin:asana_await_section", type: "sub" },
        { href: "workflow.html#tmpl-linear-await", text: "builtin:linear_await_state", type: "sub" },
        { href: "workflow.html#error-handling", text: "Error handling", type: "link" },
        { href: "workflow.html#hooks", text: "Hooks", type: "link" },
      ],
    },
    {
      title: "Reference",
      links: [
        { href: "cli.html#cli", text: "CLI commands", type: "link" },
        { href: "cli.html#cli-run", text: "erg run", type: "sub" },
        { href: "cli.html#cli-stats", text: "erg stats", type: "sub" },
        { href: "cli.html#file-layout", text: "File layout", type: "sub" },
        { href: "dashboard.html#dashboard", text: "Dashboard", type: "link" },
        { href: "dashboard.html#dashboard-features", text: "Features", type: "sub" },
        { href: "dashboard.html#dashboard-api", text: "API", type: "sub" },
        { href: "multi-repo.html#multi-repo", text: "Multi-repo", type: "link" },
        { href: "multi-repo.html#multi-machine", text: "Multi-machine claiming", type: "link" },
        { href: "multi-repo.html#services", text: "Brew services", type: "link" },
        { href: "actions.html#actions", text: "Actions", type: "link" },
        { href: "actions.html#actions-ai", text: "ai", type: "sub" },
        { href: "actions.html#actions-github", text: "github", type: "sub" },
        { href: "actions.html#actions-git", text: "git", type: "sub" },
        { href: "actions.html#actions-asana", text: "asana", type: "sub" },
        { href: "actions.html#actions-linear", text: "linear", type: "sub" },
        { href: "actions.html#actions-slack", text: "slack", type: "sub" },
        { href: "actions.html#actions-webhook", text: "webhook", type: "sub" },
        { href: "actions.html#actions-workflow", text: "workflow", type: "sub" },
        { href: "events.html#events", text: "Wait events", type: "link" },
        { href: "events.html#events-pr", text: "pr", type: "sub" },
        { href: "events.html#events-ci", text: "ci", type: "sub" },
        { href: "events.html#events-plan", text: "plan", type: "sub" },
        { href: "events.html#events-asana", text: "asana", type: "sub" },
        { href: "events.html#events-linear", text: "linear", type: "sub" },
        { href: "events.html#events-gate", text: "gate", type: "sub" },
      ],
    },
  ];

  // Determine the current page filename (e.g. "workflow.html", or "index.html" for "/").
  var path = window.location.pathname;
  var currentPage = path.substring(path.lastIndexOf("/") + 1) || "index.html";

  // Rewrite href: if the link targets the current page, strip the filename so
  // the browser treats it as an in-page anchor (required for scroll-spy).
  function localise(href) {
    var parts = href.split("#");
    if (parts[0] === currentPage) return "#" + parts[1];
    return href;
  }

  // Build sidebar HTML.
  var nav = document.getElementById("sidebar");
  if (!nav) return;

  var html =
    '<div class="sidebar-logo"><a href="index.html">erg<span>.</span></a></div>';
  html += '<ul class="sidebar-nav">';

  for (var s = 0; s < sections.length; s++) {
    var sec = sections[s];
    html += '<li class="sidebar-section">';
    html +=
      '<span class="sidebar-section-title">' + sec.title + "</span>";
    for (var i = 0; i < sec.links.length; i++) {
      var link = sec.links[i];
      var cls = link.type === "sub" ? "sidebar-sublink" : "sidebar-link";
      html +=
        '<a href="' + localise(link.href) + '" class="' + cls + '">' +
        link.text +
        "</a>";
    }
    html += "</li>";
  }

  html += "</ul>";
  html +=
    '<div class="sidebar-footer">' +
    '<a href="https://github.com/zhubert/erg">GitHub</a>' +
    '<a href="https://github.com/zhubert/erg/issues">Issues</a>' +
    "</div>";

  nav.innerHTML = html;

  // ---- Sidebar behaviour (previously duplicated per page) ----

  // Toggle sidebar (mobile)
  window.toggleSidebar = function () {
    nav.classList.toggle("open");
    document.getElementById("sidebarOverlay").classList.toggle("open");
  };

  // Scroll spy — highlight the deepest visible section link.
  var sidebarLinks = nav.querySelectorAll(".sidebar-link");
  var spySections = [];
  sidebarLinks.forEach(function (link) {
    var id = link.getAttribute("href");
    if (id && id.startsWith("#")) {
      var el = document.querySelector(id);
      if (el) spySections.push({ el: el, link: link });
    }
  });

  function updateScrollSpy() {
    var scrollY = window.scrollY + 80;
    var current = spySections[0];
    for (var i = 0; i < spySections.length; i++) {
      if (spySections[i].el.offsetTop <= scrollY) {
        current = spySections[i];
      }
    }
    sidebarLinks.forEach(function (l) {
      l.classList.remove("active");
    });
    if (current) current.link.classList.add("active");
  }

  window.addEventListener("scroll", updateScrollSpy, { passive: true });
  updateScrollSpy();

  // Close sidebar on link click (mobile).
  nav.querySelectorAll(".sidebar-link, .sidebar-sublink").forEach(function (link) {
    link.addEventListener("click", function () {
      if (window.innerWidth <= 860) {
        nav.classList.remove("open");
        document.getElementById("sidebarOverlay").classList.remove("open");
      }
    });
  });
})();
