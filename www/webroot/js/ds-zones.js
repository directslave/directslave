/*
 * Dashboard helpers for the zone table and the statistics page.
 *
 * Sorting itself is done by the server, on purpose: a secondary with ten
 * thousand zones shows fifty of them at a time, and sorting those fifty in the
 * browser would answer "which of these fifty fails most" when the question is
 * "which zones fail most". The column headings are therefore ordinary links.
 * What is left for the browser is what the browser is good at: widening the
 * click target, narrowing the visible page instantly, and reloading on a timer.
 */
(function ($) {
	'use strict';

	if (!$) {
		return;
	}

	$(function () {

		// The whole heading cell follows its link, not just the few pixels of
		// the label.
		$('#zoneTable thead th').has('a').css('cursor', 'pointer').on('click', function (event) {
			if ($(event.target).is('a')) {
				return;
			}
			var href = $(this).find('a').attr('href');
			if (href) {
				window.location = href;
			}
		});

		// Quick find narrows the rows already on the page. It deliberately does
		// not touch the URL: the search box in the header is the one that
		// searches every zone, this one only hides what is in front of you.
		var $find = $('#quickFind');
		if ($find.length) {
			var $rows = $('#zoneTable tbody tr');

			$find.on('input keyup', function () {
				var needle = $.trim($(this).val()).toLowerCase();

				if (needle === '') {
					$rows.show();
					return;
				}

				$rows.each(function () {
					var row = $(this);
					row.toggle(row.text().toLowerCase().indexOf(needle) !== -1);
				});
			});

			// Reloads land with the box empty, so nothing is ever hidden by a
			// filter the operator cannot see.
			$find.val('');
		}

		// Auto refresh. The target is the Refresh link, so the reload keeps the
		// current sort and filters instead of dropping back to the default view.
		var $auto = $('#autoRefresh');
		if (!$auto.length) {
			return;
		}

		var period = 10;
		var left = period;
		var timer = null;
		var $target = $('#refreshNow');
		var $left = $('#autoLeft');

		function stop() {
			if (timer) {
				window.clearInterval(timer);
				timer = null;
			}
			$left.text('');
		}

		function tick() {
			left -= 1;
			if (left <= 0) {
				stop();
				window.location = $target.attr('href') || window.location.href;
				return;
			}
			$left.text('(' + left + 's)');
		}

		function start() {
			stop();
			left = period;
			$left.text('(' + left + 's)');
			timer = window.setInterval(tick, 1000);
		}

		function remember(on) {
			try {
				window.localStorage.setItem('ds-auto-refresh', on ? '1' : '0');
			} catch (error) {
				// Private mode, blocked storage: the checkbox still works, it
				// just does not survive the next page.
			}
		}

		function remembered() {
			try {
				return window.localStorage.getItem('ds-auto-refresh') === '1';
			} catch (error) {
				return false;
			}
		}

		$auto.on('change', function () {
			var on = $(this).is(':checked');
			remember(on);
			if (on) {
				start();
			} else {
				stop();
			}
		});

		if (remembered()) {
			$auto.prop('checked', true);
			start();
		}
	});

}(window.jQuery));
