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
		buyShirt.addEventListener('click', requestServerRender);
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

	// requestServerRender POSTs the current text inputs to the Go server's
	// /api/render endpoint and replaces the page body with the resulting PNG.
	// Mirrors the look of createImage() but uses the print-quality server
	// render rather than the in-browser canvas export. The eventual Printful
	// flow will hand this PNG URL to the mockup endpoint; for now the button
	// just lets you see what the server produced.
	async function requestServerRender() {
		const main = document.querySelector('#main-input').value || '';
		const middle = document.querySelector('#highlight-input').value || '';
		try {
			const resp = await fetch('/api/render', {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ text: main, middletext: middle }),
			});
			if (!resp.ok) {
				const err = await resp.text();
				console.error('render failed', resp.status, err);
				alert('Render failed: ' + resp.status + ' ' + err);
				return;
			}
			const data = await resp.json();
			document.body.innerHTML = '<div class="scroller"><img src="' + data.url + '"/></div>';
		} catch (e) {
			console.error('render error', e);
			alert('Render error: ' + e.message);
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
