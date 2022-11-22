window.onload = function() {
	const DEFAULT_COPY = 'THANK YOU';
	const PAD_AMOUNT = 100;
	const STRING_LENGTH_LIMIT = 10;
	const searchParams = new URLSearchParams(window.location.search);

	init();

	document
		.querySelector('#main-input')
		.addEventListener('keyup', event => {
			const highlightInputValue = document.querySelector('#highlight-input').value;
			const newMainValue = event.target.value;
			if (newMainValue.length >= STRING_LENGTH_LIMIT) {
				return;
			}

			if (!highlightInputValue && !newMainValue) {
				return resetAll('tspan');
			} else if (highlightInputValue && !newMainValue) {
				return resetAll('.hollow-text');
			}

			let selector = highlightInputValue ? '.hollow-text' : 'tspan';

			Array
				.from(document.querySelectorAll(selector))
				.forEach(t => t.textContent = newMainValue.toUpperCase());
			
			searchParams.set('text', newMainValue);
			var newRelativePathQuery = window.location.pathname + '?' + searchParams.toString();
			history.pushState(null, '', newRelativePathQuery);
			resizeSVG();
		});

	document
		.querySelector('#highlight-input')
		.addEventListener('keyup', event => {
			if (event.target.value && event.target.value.length < STRING_LENGTH_LIMIT) {
				document.querySelector('#filled-text').textContent = event.target.value.toUpperCase();
				searchParams.set('middletext', event.target.value);
				var newRelativePathQuery = window.location.pathname + '?' + searchParams.toString();
				history.pushState(null, '', newRelativePathQuery);

				resizeSVG();
			} else if (!event.target.value && document.querySelector('tspan').textContent) {
				document.querySelector('#filled-text').textContent = document.querySelector('tspan').innerHTML;
				searchParams.set('middletext', '');
				var newRelativePathQuery = window.location.pathname + '?' + searchParams.toString();
				history.pushState(null, '', newRelativePathQuery);
				resizeSVG();
			} else {
				resetAll();
			}
		});

	document
		.getElementById('export')
		.addEventListener('click', createImage);

	function init() {
		Array
			.from(document.querySelectorAll('input'))
			.forEach(input => input.value = '');

		if (document.location.search) {

			const queryStrings = Object.fromEntries(searchParams.entries());
			console.log(queryStrings)
			document.querySelector('#main-input').value = queryStrings.text;

			Array
				.from(document.querySelectorAll('tspan'))
				.forEach(t => t.textContent = queryStrings.text);

			if (queryStrings.middletext) {
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

		const maxTextWidth = Math.ceil(text.width);
		const maxTextHeight = Math.ceil(text.height); //this will not change between the different lines
		console.log({maxTextWidth, maxTextHeight})
		svg.setAttribute('width', `${maxTextWidth}px`);
		svg.setAttribute('height', `${maxTextHeight}px`);
	}

	function createImage() {
		const X_IMAGE_OFFSET = 20;
		const svg = document.querySelector('svg');
		const text = document.querySelector('text');
		let image = new Image();
		const { width } = text.getBBox();
		const height = document.querySelector('tspan').dy.baseVal[0].value * 7;
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
			document.body.innerHTML = '<img src="'+png+'"/>';
		}
		image.src = blobUrl;


	}

}
