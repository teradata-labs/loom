// Make Mermaid diagrams zoomable by clicking
document.addEventListener('DOMContentLoaded', function() {
    document.querySelectorAll('.mermaid').forEach(function(diagram) {
        diagram.addEventListener('click', function() {
            this.classList.toggle('zoomed');
        });
    });
});
