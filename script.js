window.onload = function() {
	const DEFAULT = 'THANK YOU';

	Array
		.from(document.querySelectorAll('input'))
		.forEach(input => input.value = '');


	function resetAll(selector) {
		Array
			.from(document.querySelectorAll(selector))
			.forEach(p => p.innerHTML = 'THANK YOU');
	}

	document
		.querySelector('#main-input')
		.addEventListener('keyup', event => {
			const highlightInputValue = document.querySelector('#highlight-input').value;
			const newMainValue = event.target.value;

			if (!highlightInputValue && !newMainValue) {
				return resetAll('p');
			} else if (highlightInputValue && !newMainValue) {
				return resetAll('.hollow-text');
			}

			let selector = highlightInputValue ? '.hollow-text' : 'p';

			Array
				.from(document.querySelectorAll(selector))
				.forEach(p => p.innerHTML = newMainValue.toUpperCase());
		});

	document
		.querySelector('#highlight-input')
		.addEventListener('keyup', event => {
			if (event.target.value) {
				document.querySelector('#filled-text').innerHTML = event.target.value.toUpperCase();

			} else if (!event.target.value && document.querySelector('p').innerHTML) {
				console.log('it is empty and there is other html')
				document.querySelector('#filled-text').innerHTML = document.querySelector('p').innerHTML;
			} else {
				resetAll();
			}

		});

}
