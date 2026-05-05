window.onload = function () {
	const DEFAULT_COPY = 'THANK YOU';
	const PAD_AMOUNT = 100;
	const searchParams = new URLSearchParams(window.location.search);

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
			.from(document.querySelectorAll('input'))
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

	// createTShirt POSTs the current text inputs to /api/printful/products,
	// which renders+saves the PNG and (in parallel) creates a Printful mockup
	// task and a Printful sync product. On success the page swaps to a status
	// view showing links and the polled mockup image. The endpoint always
	// returns a usable file_id/file_url even on the 503 unconfigured path.
	const MOCKUP_POLL_INTERVAL_MS = 1500;
	const MOCKUP_POLL_TIMEOUT_MS = 5 * 60 * 1000;
	let renderInflight = false;
	async function createTShirt() {
		if (renderInflight) return;
		renderInflight = true;
		const button = document.getElementById('buy-shirt');
		if (button) button.classList.add('is-loading');
		const main = document.querySelector('#main-input').value || '';
		const middle = document.querySelector('#highlight-input').value || '';
		try {
			const resp = await fetch('/api/printful/products', {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ text: main, middletext: middle }),
			});
			let data = null;
			try { data = await resp.json(); } catch (_) { data = {}; }

			if (resp.status === 503) {
				// Server not configured — show the saved design anyway.
				renderUnconfigured(data);
				return;
			}
			if (resp.status === 502 && data && data.partial) {
				renderPartial(data);
				return;
			}
			if (!resp.ok) {
				const detail = (data && (data.message || data.error)) || resp.statusText;
				console.error('createTShirt failed', resp.status, detail);
				alert('Create T-Shirt failed: ' + resp.status + ' ' + detail);
				return;
			}
			renderSuccess(data);
		} catch (e) {
			console.error('createTShirt error', e);
			alert('Something went wrong, please try again.');
		} finally {
			renderInflight = false;
			if (button) button.classList.remove('is-loading');
		}
	}

	function renderSuccess(data) {
		const productLink = data.sync_product_id
			? '<p><a href="https://www.printful.com/dashboard/sync/products/' + data.sync_product_id + '" target="_blank">Printful product #' + data.sync_product_id + '</a></p>'
			: '';
		const fileLink = '<p><a href="' + data.file_url + '" target="_blank">View print file</a></p>';
		document.body.innerHTML =
			'<div class="scroller">' +
			'<h1>T-Shirt Created</h1>' +
			productLink +
			fileLink +
			'<div id="mockup-status"><p>Generating mockup...</p></div>' +
			'</div>';
		if (data.mockup_status_url) {
			pollMockup(data.mockup_status_url);
		}
	}

	function renderUnconfigured(data) {
		const fileLink = data && data.file_url
			? '<p><a href="' + data.file_url + '" target="_blank">View saved design</a></p>'
			: '';
		document.body.innerHTML =
			'<div class="scroller">' +
			'<h1>Design saved</h1>' +
			'<p>Server not configured for Printful — your design was saved.</p>' +
			fileLink +
			'</div>';
	}

	function renderPartial(data) {
		const p = data.partial || {};
		const what = [];
		if (p.mockup_ok) what.push('mockup OK');
		else what.push('mockup failed');
		if (p.sync_product_ok) what.push('sync product OK');
		else what.push('sync product failed');
		const fileLink = data.file_url
			? '<p><a href="' + data.file_url + '" target="_blank">View saved design</a></p>'
			: '';
		document.body.innerHTML =
			'<div class="scroller">' +
			'<h1>T-Shirt created partially</h1>' +
			'<p>' + what.join(' &middot; ') + '</p>' +
			fileLink +
			'<button id="retry-btn" class="styled-button">Retry</button>' +
			'</div>';
		const retry = document.getElementById('retry-btn');
		if (retry) retry.addEventListener('click', () => location.reload());
	}

	async function pollMockup(statusURL) {
		const start = Date.now();
		while (Date.now() - start < MOCKUP_POLL_TIMEOUT_MS) {
			await new Promise(r => setTimeout(r, MOCKUP_POLL_INTERVAL_MS));
			let data;
			try {
				const resp = await fetch(statusURL);
				if (!resp.ok) {
					updateMockupStatus('Mockup polling error: HTTP ' + resp.status);
					return;
				}
				data = await resp.json();
			} catch (e) {
				updateMockupStatus('Mockup polling error.');
				return;
			}
			if (data.status === 'completed') {
				const url = extractMockupURL(data);
				if (url) {
					updateMockupStatus('<img src="' + url + '" alt="mockup" style="max-width:100%"/>');
				} else {
					updateMockupStatus('Mockup completed but no image URL was returned.');
				}
				return;
			}
			if (data.status === 'failed') {
				const reasons = (data.failure_reasons || []).join(', ');
				updateMockupStatus('Mockup failed' + (reasons ? ': ' + reasons : '.'));
				return;
			}
		}
		updateMockupStatus('Mockup timed out (5 min).');
	}

	function extractMockupURL(data) {
		if (!data || !data.catalog_variant_mockups) return null;
		for (const v of data.catalog_variant_mockups) {
			if (!v.mockups) continue;
			for (const m of v.mockups) {
				if (m.mockup_url) return m.mockup_url;
				if (m.placement_url) return m.placement_url;
			}
		}
		return null;
	}

	function updateMockupStatus(html) {
		const el = document.getElementById('mockup-status');
		if (el) el.innerHTML = html;
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
