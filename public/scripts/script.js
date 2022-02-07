window.onload = function() {
	const DEFAULT_COPY = 'THANK YOU';
	const PAD_AMOUNT = 50;
	const SCALE_AMOUNT = 2.0
	resizeSVG();

	Array
		.from(document.querySelectorAll('input'))
		.forEach(input => input.value = '');


	function resetAll(selector) {
		Array
			.from(document.querySelectorAll(selector))
			.forEach(t => t.textContent = DEFAULT_COPY);
		resizeSVG();
	}

	document
		.querySelector('#main-input')
		.addEventListener('keyup', event => {
			const highlightInputValue = document.querySelector('#highlight-input').value;
			const newMainValue = event.target.value;
			if (newMainValue.length > 10) return;

			if (!highlightInputValue && !newMainValue) {
				return resetAll('text');
			} else if (highlightInputValue && !newMainValue) {
				return resetAll('.hollow-text');
			}

			let selector = highlightInputValue ? '.hollow-text' : 'text';

			Array
				.from(document.querySelectorAll(selector))
				.forEach(t => t.textContent = newMainValue.toUpperCase());
			resizeSVG();
		});

	document
		.querySelector('#highlight-input')
		.addEventListener('keyup', event => {
			if (event.target.value && event.target.value.length < 11) {
				document.querySelector('#filled-text').textContent = event.target.value.toUpperCase();
				resizeSVG();
			} else if (!event.target.value && document.querySelector('text').textContent) {
				document.querySelector('#filled-text').textContent = document.querySelector('text').innerHTML;
				resizeSVG();
			} else {
				resetAll();
			}

		});


	document
		.getElementById('export')
		.addEventListener('click', createImage);


	function resizeSVG() {
		let svg = document.querySelector('svg');
		let texts = Array.
						from(document
								.querySelectorAll('text'))
								.map(text => text.getBBox())
								.sort((a,b) => a.width < b.width);

		const maxTextWidth = Math.ceil(texts[0].width);
		const maxTextHeight = Math.ceil(texts[0].height); //this will not change between the different lines
		console.log({maxTextWidth, maxTextHeight})
		svg.setAttribute('width', `${maxTextWidth + PAD_AMOUNT}px`);
		svg.setAttribute('height', `${maxTextHeight * 4 + PAD_AMOUNT - 20}px`);
	}

	function createImage() {
		const svg = document.querySelector('svg');
		// svg.setAttribute('transform', `scale(${SCALE_AMOUNT})`);
		let image = new Image();
		const { width, height } = svg.getBBox();
		console.log(svg.getBBox())
		const clone = svg.cloneNode(true);
		const blob = new Blob([clone.outerHTML], { type: 'image/svg+xml;charset=utf-8' });
		const URL = window.URL || window.webkitURL || window;
		const blobUrl = URL.createObjectURL(blob);

		let canvas = document.createElement('canvas');
		canvas.width = SCALE_AMOUNT * width + PAD_AMOUNT;
		canvas.height = SCALE_AMOUNT * height;
		console.log(canvas)
		let context = canvas.getContext('2d');

		image.onload = () => {
			context.drawImage(image, 0, 0);
			let png = canvas.toDataURL('image/png');
			document.body.innerHTML = '<img src="'+png+'"/>';
		}
		image.src = blobUrl;


	}

}
