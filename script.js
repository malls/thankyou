window.onload = function() {
	const DEFAULT = 'THANK YOU';

	Array
		.from(document.querySelectorAll('input'))
		.forEach(input => input.value = '');


	function resetAll(selector) {
		Array
			.from(document.querySelectorAll(selector))
			.forEach(t => t.textContent = 'THANK YOU');
	}

	document
		.querySelector('#main-input')
		.addEventListener('keyup', event => {
			const highlightInputValue = document.querySelector('#highlight-input').value;
			const newMainValue = event.target.value;

			if (!highlightInputValue && !newMainValue) {
				return resetAll('text');
			} else if (highlightInputValue && !newMainValue) {
				return resetAll('.hollow-text');
			}

			let selector = highlightInputValue ? '.hollow-text' : 'text';

			Array
				.from(document.querySelectorAll(selector))
				.forEach(t => t.textContent = newMainValue.toUpperCase());
		});

	document
		.querySelector('#highlight-input')
		.addEventListener('keyup', event => {
			if (event.target.value) {
				document.querySelector('#filled-text').textContent = event.target.value.toUpperCase();

			} else if (!event.target.value && document.querySelector('text').textContent) {
				document.querySelector('#filled-text').textContent = document.querySelector('text').innerHTML;
			} else {
				resetAll();
			}

		});


	document
		.getElementById('export')
		.addEventListener('click', async event => {
			const svg = document.querySelector('svg');
			const { width, height } = svg.getBBox();

			const clone = svg.cloneNode(true);
			const blob = new Blob([clone.outerHML], { type: 'image/svg+xml;charset=utf-8'});
			let URL = window.URL || window.webkitURL || window;
			let blobURL = URL.createObjectURL(blob);

			// window.location = blobURL

			let image = createEmptyImage(Math.ceil(width), Math.ceil(height));
			console.log(image)
			image.src = blobURL;
			document.getElementById('result-section').appendChild(image);
		});


		function createEmptyImage(width, height) {
			let img = new Image();
			img.onload = () => {
				let canvas = document.createElement('canvas');
				canvas.width = width;
				canvas.height = height;
				let context = canvas.getContext('2d');
				context.drawImage(img, 0, 0, width, height);
			}
			return img;
		}

}
