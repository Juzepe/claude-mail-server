/* ============================================================
   Mail Server Admin UI — App JS
   ============================================================ */

'use strict';

// ---- Copy to clipboard ----

/**
 * Copies the given text to the clipboard and shows a toast notification.
 * @param {string} text - Text to copy
 */
function copyText(text) {
    if (!text) return;
    text = text.trim();

    const doShowToast = () => {
        const toast = document.getElementById('copyToast');
        if (!toast) return;
        toast.classList.add('show');
        setTimeout(() => toast.classList.remove('show'), 2000);
    };

    if (navigator.clipboard && navigator.clipboard.writeText) {
        navigator.clipboard.writeText(text).then(doShowToast).catch(() => {
            fallbackCopy(text);
            doShowToast();
        });
    } else {
        fallbackCopy(text);
        doShowToast();
    }
}

/**
 * Copies text from a <pre> or <code> element by its ID.
 * @param {string} elementId - ID of the element to copy from
 */
function copyCode(elementId) {
    const el = document.getElementById(elementId);
    if (!el) return;
    copyText(el.textContent || el.innerText);
}

/**
 * Fallback copy using textarea + execCommand (legacy browsers).
 * @param {string} text
 */
function fallbackCopy(text) {
    const textarea = document.createElement('textarea');
    textarea.value = text;
    textarea.style.cssText = 'position:fixed;top:-9999px;left:-9999px;opacity:0';
    document.body.appendChild(textarea);
    textarea.focus();
    textarea.select();
    try {
        document.execCommand('copy');
    } catch (e) {
        console.warn('Copy failed:', e);
    }
    document.body.removeChild(textarea);
}

// ---- Delete confirmation ----

/**
 * Confirms deletion of a mail user with a browser dialog.
 * @param {string} email - Email address to delete
 * @returns {boolean} - True if confirmed
 */
function confirmDelete(email) {
    return confirm(
        'Delete email account "' + email + '"?\n\n' +
        'This will remove the account from the server. ' +
        'Existing emails in the maildir will NOT be deleted automatically.'
    );
}

// ---- Flash message auto-dismiss ----

/**
 * Auto-dismisses flash messages after a timeout.
 */
function initFlashDismiss() {
    const flashMessages = document.querySelectorAll('.flash-banner, #flashMsg');
    flashMessages.forEach(function(el) {
        // Auto dismiss after 5 seconds
        setTimeout(function() {
            el.style.transition = 'opacity 0.5s ease, max-height 0.5s ease, margin 0.5s ease';
            el.style.opacity = '0';
            el.style.maxHeight = '0';
            el.style.marginBottom = '0';
            el.style.overflow = 'hidden';
            setTimeout(function() { el.remove(); }, 500);
        }, 5000);
    });
}

// ---- Sidebar toggle (mobile) ----

/**
 * Toggles the sidebar open/closed on mobile.
 */
function toggleSidebar() {
    const sidebar = document.getElementById('sidebar');
    const overlay = document.getElementById('sidebarOverlay');
    if (!sidebar) return;
    sidebar.classList.toggle('open');
    if (overlay) {
        overlay.classList.toggle('open');
    }
}

// ---- Active nav item highlight ----

/**
 * Highlights the correct sidebar nav item based on current URL path.
 */
function highlightActiveNav() {
    const path = window.location.pathname;
    const navItems = document.querySelectorAll('.nav-item');
    navItems.forEach(function(item) {
        const href = item.getAttribute('href');
        if (!href) return;
        // Exact match for root, prefix match for others
        if (href === '/' ? path === '/' : path.startsWith(href)) {
            item.classList.add('active');
        }
    });
}

// ---- Email viewer toggle ----

/**
 * Shows or hides the email viewer pane (for mobile).
 */
function showEmailViewer() {
    const viewer = document.getElementById('emailViewer');
    if (viewer) {
        viewer.scrollIntoView({ behavior: 'smooth', block: 'start' });
    }
}

// ---- Password strength indicator ----

/**
 * Sets up basic password strength indicator on the add user form.
 */
function initPasswordStrength() {
    const pwInput = document.getElementById('password');
    if (!pwInput) return;

    const indicator = document.createElement('div');
    indicator.className = 'pw-strength';
    indicator.style.cssText = 'height:3px;border-radius:2px;margin-top:6px;transition:all 0.3s;background:#30363d;';
    pwInput.parentNode.insertBefore(indicator, pwInput.nextSibling);

    pwInput.addEventListener('input', function() {
        const val = pwInput.value;
        let strength = 0;
        if (val.length >= 8)  strength++;
        if (val.length >= 12) strength++;
        if (/[A-Z]/.test(val)) strength++;
        if (/[0-9]/.test(val)) strength++;
        if (/[^A-Za-z0-9]/.test(val)) strength++;

        const colors = ['#30363d', '#f85149', '#d29922', '#3fb950', '#3fb950'];
        const widths  = ['0%', '25%', '50%', '75%', '100%'];
        indicator.style.background = colors[strength];
        indicator.style.width = widths[strength];
    });
}

// ---- Confirm password validation ----

function initConfirmPassword() {
    const pw  = document.getElementById('password');
    const pw2 = document.getElementById('confirm_password');
    const err = document.getElementById('pwMatchError');
    if (!pw || !pw2 || !err) return;

    const check = function() {
        if (pw2.value && pw.value !== pw2.value) {
            err.style.display = 'block';
            pw2.setCustomValidity('Passwords do not match');
        } else {
            err.style.display = 'none';
            pw2.setCustomValidity('');
        }
    };

    pw.addEventListener('input', check);
    pw2.addEventListener('input', check);
}

// ---- DNS record copy feedback ----

/**
 * Adds visual feedback to copy buttons when clicked.
 */
function initCopyButtons() {
    document.querySelectorAll('.copy-btn').forEach(function(btn) {
        btn.addEventListener('click', function() {
            const originalHTML = btn.innerHTML;
            btn.innerHTML = '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polyline points="20 6 9 17 4 12"/></svg>';
            btn.style.color = 'var(--success)';
            btn.style.borderColor = 'var(--success)';
            setTimeout(function() {
                btn.innerHTML = originalHTML;
                btn.style.color = '';
                btn.style.borderColor = '';
            }, 1500);
        });
    });
}

// ---- Init ----

document.addEventListener('DOMContentLoaded', function() {
    initFlashDismiss();
    highlightActiveNav();
    initPasswordStrength();
    initConfirmPassword();
    initCopyButtons();
});
