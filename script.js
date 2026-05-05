window.onload = function () {
	const DEFAULT_COPY = 'THANK YOU';
	const PAD_AMOUNT = 100;
	const searchParams = new URLSearchParams(window.location.search);

	// Thanks page state takes precedence over the design UI: when Stripe
	// redirects back with ?session_id=..., swap to a static thank-you view.
	// Same for ?canceled=1 — render a "checkout canceled" message but
	// preserve the design state (text/middletext) for a quick retry.
	if (searchParams.has('session_id')) {
		renderThanksState(searchParams.get('session_id'));
		return;
	}
	if (searchParams.get('canceled') === '1') {
		renderCanceledState();
		// fall through so the design UI still wires up — the canceled
		// banner sits above the editor.
	}

	init();

	document
		.querySelector('#main-input')
		.addEventListener('keyup', event => {
			const highlightInputValue = document.querySelector('#highlight-input').value;
			const newMainValue = event.target.value;

			if (!newMainValue) {
				searchParams.delete('text');

				let newRelativePathQuery = window.location.pathname + '?' + searchParams.toString();
				history.pushState(null, '', newRelativePathQuery);

				if (!highlightInputValue) {
					return resetAll('.main-text');
				} else if (highlightInputValue) {
					return resetAll('.hollow');
				}
			}

			let selector = highlightInputValue ? '.hollow' : '.main-text';

			Array
				.from(document.querySelectorAll(selector))
				.forEach(t => t.textContent = newMainValue);

			searchParams.set('text', newMainValue);
			var newRelativePathQuery = window.location.pathname + '?' + searchParams.toString();
			history.pushState(null, '', newRelativePathQuery);
			resizeSVG();
		});

	document
		.querySelector('#highlight-input')
		.addEventListener('keyup', event => {
			if (event.target.value) {
				document.querySelector('#filled-text').textContent = event.target.value;
				searchParams.set('middletext', event.target.value);
				let newRelativePathQuery = window.location.pathname + '?' + searchParams.toString();
				history.pushState(null, '', newRelativePathQuery);

				resizeSVG();
			} else if (!event.target.value && document.querySelector('.main-text').textContent) {
				document.querySelector('#filled-text').textContent = document.querySelector('.main-text').innerHTML;
				searchParams.delete('middletext', '');
				let newRelativePathQuery = window.location.pathname + '?' + searchParams.toString();
				history.pushState(null, '', newRelativePathQuery);
				resizeSVG();
			} else {
				resetAll();
			}
		});

	document
		.getElementById('export')
		.addEventListener('click', createImage);

	const buyShirt = document.getElementById('buy-shirt');
	if (buyShirt) {
		buyShirt.addEventListener('click', createTShirt);
	}

	function init() {
		Array
			.from(document.querySelectorAll('input[type="text"]'))
			.forEach(input => input.value = '');

		if (document.location.search) {
			const queryStrings = Object.fromEntries(searchParams.entries());

			if (queryStrings.text) {
				document.querySelector('#main-input').value = queryStrings.text;

				Array
					.from(document.querySelectorAll('.main-text'))
					.forEach(t => t.textContent = queryStrings.text);

			} else {
				Array
					.from(document.querySelectorAll('.main-text'))
					.forEach(t => t.textContent = 'THANK YOU');
			}

			if (queryStrings.middletext) {
				queryStrings.middletext = queryStrings.middletext;
				document.querySelector('#highlight-input').value = queryStrings.middletext;
				document.getElementById('filled-text').textContent = queryStrings.middletext;
			}
		}

		resizeSVG();
	}

	function resetAll(selector) {
		Array
			.from(document.querySelectorAll(selector))
			.forEach(t => t.textContent = DEFAULT_COPY);
		resizeSVG();
	}

	function resizeSVG() {
		let svg = document.querySelector('svg');
		let text = document.querySelector('text').getBBox();

		const maxTextWidth = Math.max(Math.ceil(text.width), 2000);
		const maxTextHeight = Math.ceil(text.height); //this will not change between the different lines
		console.log({ maxTextWidth, maxTextHeight })
		svg.setAttribute('width', `${maxTextWidth}px`);
		svg.setAttribute('height', `${maxTextHeight}px`);
	}

	// readVariantCatalog parses the embedded <script id="variant-catalog">
	// JSON. Returned shape: {"S":4011,"M":4012,...}. When the catalog isn't
	// present (older builds) or fails to parse, returns null and the buy
	// flow shows a clear error.
	function readVariantCatalog() {
		const el = document.getElementById('variant-catalog');
		if (!el) return null;
		try {
			return JSON.parse(el.textContent);
		} catch (e) {
			console.error('variant-catalog parse failed', e);
			return null;
		}
	}

	// readSelectedVariantID resolves the size radio choice to a Printful
	// variant_id via the embedded catalog. Returns 0 if the catalog has a
	// 0 placeholder for the selected size — the server then 503s with
	// variant_catalog_incomplete and the user sees a "shop not configured"
	// banner. Default is M when no radio is checked.
	function readSelectedVariantID() {
		const catalog = readVariantCatalog();
		if (!catalog) return 0;
		const checked = document.querySelector('#size-picker input[name="size"]:checked');
		const size = checked ? checked.value : 'M';
		const id = catalog[size];
		return typeof id === 'number' ? id : 0;
	}

	// showError renders an inline error banner near the buy button and
	// auto-clears on the next click. Replaces the previous alert()-based
	// flow which was blocking and felt broken on mobile.
	function showError(msg) {
		const el = document.getElementById('buy-error');
		if (!el) {
			console.error('buy-error div missing; falling back to alert');
			alert(msg);
			return;
		}
		el.textContent = msg;
		el.hidden = false;
	}
	function clearError() {
		const el = document.getElementById('buy-error');
		if (el) {
			el.hidden = true;
			el.textContent = '';
		}
	}

	// renderThanksState is the post-payment view. Static copy + a primary
	// "Make another →" CTA back to /. The session id appears in the URL but
	// we don't fetch order detail for V1 — copy alone is enough.
	function renderThanksState(sessionID) {
		document.body.innerHTML =
			'<div class="thanks-state">' +
			'<h1>Thanks — your order is on its way.</h1>' +
			'<p>Session: <code>' + escapeHTML(sessionID) + '</code></p>' +
			'<a class="cta" href="/">Make another →</a>' +
			'</div>';
	}

	// renderCanceledState swaps a small banner above the editor when the
	// user backed out of Stripe Checkout. Doesn't replace the page (so
	// they can re-click Buy without re-entering text).
	function renderCanceledState() {
		const banner = document.createElement('div');
		banner.className = 'buy-error';
		banner.textContent = 'Checkout was canceled. Try again when you’re ready.';
		document.body.insertBefore(banner, document.body.firstChild);
	}

	function escapeHTML(s) {
		return String(s).replace(/[&<>"']/g, ch => ({
			'&': '&amp;',
			'<': '&lt;',
			'>': '&gt;',
			'"': '&quot;',
			"'": '&#39;',
		}[ch]));
	}

	// createTShirt POSTs the current text + selected variant to
	// /api/checkout/start. The server renders the PNG, creates a Printful
	// sync_product, opens a Stripe Checkout Session, and returns the
	// checkout_url for us to redirect to. All upstream failures are
	// surfaced via inline #buy-error rather than blocking alert()s.
	let renderInflight = false;
	async function createTShirt() {
		if (renderInflight) return;
		renderInflight = true;
		clearError();
		const button = document.getElementById('buy-shirt');
		if (button) button.classList.add('is-loading');
		const main = document.querySelector('#main-input').value || '';
		const middle = document.querySelector('#highlight-input').value || '';
		const variantID = readSelectedVariantID();
		try {
			const resp = await fetch('/api/checkout/start', {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ text: main, middletext: middle, variant_id: variantID }),
			});
			let data = null;
			try { data = await resp.json(); } catch (_) { data = {}; }

			if (resp.status === 503) {
				if (data && data.error === 'stripe_unconfigured') {
					showError('Checkout is not configured yet. Please try again later.');
				} else if (data && data.error === 'printful_unconfigured') {
					showError('Shop is not configured yet. Please try again later.');
				} else if (data && data.error === 'variant_catalog_incomplete') {
					showError('Shirt sizes are not configured yet. Please try again later.');
				} else {
					showError('Shop is not fully configured yet. Please try again later.');
				}
				return;
			}
			if (resp.status === 502) {
				if (data && data.error === 'printful_create_failed') {
					showError('Couldn’t create your shirt. Please try again.');
				} else if (data && data.error === 'stripe_session_failed') {
					showError('Couldn’t start checkout. Please try again.');
				} else {
					showError('Something went wrong upstream. Please try again.');
				}
				return;
			}
			if (resp.status === 400) {
				const msg = (data && (data.message || data.error)) || 'Please check your inputs and try again.';
				showError(msg);
				return;
			}
			if (!resp.ok) {
				const detail = (data && (data.message || data.error)) || resp.statusText;
				showError('Error ' + resp.status + ': ' + detail);
				return;
			}
			if (!data || !data.checkout_url) {
				showError('Server returned no checkout URL. Please try again.');
				return;
			}
			window.location = data.checkout_url;
		} catch (e) {
			console.error('createTShirt error', e);
			showError('Network error. Please try again.');
		} finally {
			renderInflight = false;
			if (button) button.classList.remove('is-loading');
		}
	}

	function createImage() {
		const X_IMAGE_OFFSET = 0;
		const svg = document.querySelector('svg');
		const text = document.querySelector('text');
		let image = new Image();
		const { width } = text.getBBox();
		const height = document.querySelector('tspan').dy.baseVal[0].value * 7 + 150;
		console.log(svg.getBBox())
		console.log(text.getBBox())
		const clone = svg.cloneNode(true);
		const blob = new Blob([clone.outerHTML], { type: 'image/svg+xml;charset=utf-8' });
		const URL = window.URL || window.webkitURL || window;
		const blobUrl = URL.createObjectURL(blob);

		let canvas = document.createElement('canvas');
		canvas.width = width;
		canvas.height = height + PAD_AMOUNT;
		console.log(canvas)
		let context = canvas.getContext('2d');

		image.onload = () => {
			context.drawImage(image, X_IMAGE_OFFSET, 0);
			let png = canvas.toDataURL('image/png');
			document.body.innerHTML = '<div class="scroller"><img src="' + png + '"/></div>';
		}
		image.src = blobUrl;

	}

}
