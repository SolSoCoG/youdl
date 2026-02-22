(function() {
    var card = document.getElementById('job-card');
    if (!card) return;

    var jobId = card.dataset.jobId;
    var currentStatus = card.dataset.status;
    var intentionalNav = false;

    // Cancel job when user actually leaves/closes the page (not tab switch)
    var cancellable = ['pending', 'queued', 'running'];
    if (cancellable.indexOf(currentStatus) !== -1) {
        window.addEventListener('pagehide', function() {
            if (!intentionalNav) {
                navigator.sendBeacon('/job/' + jobId + '/cancel');
            }
        });
    }

    // Mark form submissions as intentional (format select)
    var forms = document.querySelectorAll('form');
    for (var i = 0; i < forms.length; i++) {
        forms[i].addEventListener('submit', function() { intentionalNav = true; });
    }

    // Only poll for transitional statuses
    var pollable = ['pending', 'queued', 'running'];
    if (pollable.indexOf(currentStatus) === -1) return;

    var interval = setInterval(function() {
        fetch('/api/job/' + jobId + '/status')
            .then(function(r) { return r.json(); })
            .then(function(data) {
                if (!data.job) return;
                if (data.job.status !== currentStatus) {
                    intentionalNav = true;
                    window.location.reload();
                }
            })
            .catch(function() {});
    }, 2000);
})();
