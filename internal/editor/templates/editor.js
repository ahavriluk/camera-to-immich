// State
const state = {
    images: [],
    edits: {},
    currentIndex: 0,
    globalPreset: 'color', // Default to Color
    currentAspect: 'free',
    isDragging: false,
    dragType: null,
    isLoading: false,
    useApi: true, // Set to false to use demo images for preview
    currentFilter: 'all', // 'all', 'edited', 'unedited', 'skipped'
};

// WebGL Exposure Renderer - GPU-accelerated exposure preview
// Performs exposure compensation in linear light space with filmic highlight rolloff,
// matching RawTherapee's behavior more closely than CSS brightness()
let exposureRenderer = null;

class ExposureRenderer {
    constructor(canvas) {
        this.canvas = canvas;
        this.gl = canvas.getContext('webgl', { preserveDrawingBuffer: true })
              || canvas.getContext('experimental-webgl', { preserveDrawingBuffer: true });
        if (!this.gl) {
            console.warn('WebGL not supported, falling back to CSS filters');
            this.supported = false;
            return;
        }
        this.supported = true;
        this.texture = null;
        this.currentImageSrc = null;
        this._initShaders();
        this._initGeometry();
    }

    _initShaders() {
        const gl = this.gl;

        const vsSource = `
            attribute vec2 aPosition;
            varying vec2 vTexCoord;
            void main() {
                vTexCoord = (aPosition + 1.0) / 2.0;
                vTexCoord.y = 1.0 - vTexCoord.y;
                gl_Position = vec4(aPosition, 0.0, 1.0);
            }
        `;

        const fsSource = `
            precision mediump float;
            uniform sampler2D uImage;
            uniform float uExposure;
            uniform bool uGrayscale;
            varying vec2 vTexCoord;

            vec3 srgbToLinear(vec3 c) {
                vec3 lo = c / 12.92;
                vec3 hi = pow((c + 0.055) / 1.055, vec3(2.4));
                return mix(lo, hi, step(0.04045, c));
            }

            vec3 linearToSrgb(vec3 c) {
                vec3 lo = 12.92 * c;
                vec3 hi = 1.055 * pow(c, vec3(1.0 / 2.4)) - 0.055;
                return mix(lo, hi, step(0.0031308, c));
            }

            void main() {
                vec4 texel = texture2D(uImage, vTexCoord);
                vec3 linear = srgbToLinear(texel.rgb);

                // Apply exposure in linear light space (correct photographic behavior)
                float multiplier = pow(2.0, uExposure);
                linear *= multiplier;

                // Filmic highlight rolloff (modified Reinhard tonemapping)
                // Prevents harsh highlight clipping that CSS brightness() causes
                // At EV=0: identity (no change). At high values: soft shoulder.
                vec3 tonemapped = linear / (linear + 1.0);
                // Normalize so EV=0 produces identity output
                // At EV=0, input=x, output = x/(x+1) / (1/(1+1)) = 2x/(x+1)
                // For small x (shadows/midtones): ≈ 2x * (1-x) ≈ identity
                float normFactor = 1.0 / (1.0 + 1.0); // = 0.5
                tonemapped /= normFactor;

                vec3 result = linearToSrgb(clamp(tonemapped, 0.0, 1.0));

                // B&W conversion using Rec. 709 luminance weights
                if (uGrayscale) {
                    float lum = dot(result, vec3(0.2126, 0.7152, 0.0722));
                    // Slight contrast boost typical of B&W film
                    lum = clamp((lum - 0.5) * 1.1 + 0.5, 0.0, 1.0);
                    result = vec3(lum);
                }

                gl_FragColor = vec4(result, texel.a);
            }
        `;

        const vs = this._compileShader(gl.VERTEX_SHADER, vsSource);
        const fs = this._compileShader(gl.FRAGMENT_SHADER, fsSource);

        if (!vs || !fs) {
            this.supported = false;
            return;
        }

        this.program = gl.createProgram();
        gl.attachShader(this.program, vs);
        gl.attachShader(this.program, fs);
        gl.linkProgram(this.program);

        if (!gl.getProgramParameter(this.program, gl.LINK_STATUS)) {
            console.error('Shader link error:', gl.getProgramInfoLog(this.program));
            this.supported = false;
            return;
        }

        this.loc = {
            aPosition: gl.getAttribLocation(this.program, 'aPosition'),
            uImage: gl.getUniformLocation(this.program, 'uImage'),
            uExposure: gl.getUniformLocation(this.program, 'uExposure'),
            uGrayscale: gl.getUniformLocation(this.program, 'uGrayscale'),
        };
    }

    _compileShader(type, source) {
        const gl = this.gl;
        const shader = gl.createShader(type);
        gl.shaderSource(shader, source);
        gl.compileShader(shader);
        if (!gl.getShaderParameter(shader, gl.COMPILE_STATUS)) {
            console.error('Shader compile error:', gl.getShaderInfoLog(shader));
            gl.deleteShader(shader);
            return null;
        }
        return shader;
    }

    _initGeometry() {
        const gl = this.gl;
        // Full-screen quad (two triangles covering clip space -1..1)
        const vertices = new Float32Array([
            -1, -1,   1, -1,   -1,  1,
            -1,  1,   1, -1,    1,  1,
        ]);
        this.vertexBuffer = gl.createBuffer();
        gl.bindBuffer(gl.ARRAY_BUFFER, this.vertexBuffer);
        gl.bufferData(gl.ARRAY_BUFFER, vertices, gl.STATIC_DRAW);
    }

    // Upload image as GPU texture. Call once per image (not per slider move).
    loadImage(img) {
        if (!this.supported) return;
        if (this.currentImageSrc === img.src && this.texture) return;

        const gl = this.gl;

        // Set internal canvas resolution to match image
        this.canvas.width = img.naturalWidth;
        this.canvas.height = img.naturalHeight;
        gl.viewport(0, 0, this.canvas.width, this.canvas.height);

        if (!this.texture) {
            this.texture = gl.createTexture();
        }
        gl.bindTexture(gl.TEXTURE_2D, this.texture);
        gl.texImage2D(gl.TEXTURE_2D, 0, gl.RGBA, gl.RGBA, gl.UNSIGNED_BYTE, img);

        // Required for non-power-of-2 textures (which all photos are)
        gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_S, gl.CLAMP_TO_EDGE);
        gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_T, gl.CLAMP_TO_EDGE);
        gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.LINEAR);
        gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.LINEAR);

        this.currentImageSrc = img.src;
    }

    // Render with given settings. This is fast (<1ms) — call on every slider change.
    render(exposure, grayscale) {
        if (!this.supported || !this.texture) return;

        const gl = this.gl;
        gl.useProgram(this.program);

        gl.activeTexture(gl.TEXTURE0);
        gl.bindTexture(gl.TEXTURE_2D, this.texture);
        gl.uniform1i(this.loc.uImage, 0);

        gl.uniform1f(this.loc.uExposure, exposure);
        gl.uniform1i(this.loc.uGrayscale, grayscale ? 1 : 0);

        gl.bindBuffer(gl.ARRAY_BUFFER, this.vertexBuffer);
        gl.enableVertexAttribArray(this.loc.aPosition);
        gl.vertexAttribPointer(this.loc.aPosition, 2, gl.FLOAT, false, 0, 0);

        gl.drawArrays(gl.TRIANGLES, 0, 6);
    }

    // Force re-upload of the current image texture (e.g., after image src changes)
    invalidate() {
        this.currentImageSrc = null;
    }

    destroy() {
        if (!this.gl) return;
        if (this.texture) this.gl.deleteTexture(this.texture);
        if (this.program) this.gl.deleteProgram(this.program);
        if (this.vertexBuffer) this.gl.deleteBuffer(this.vertexBuffer);
    }
}

// Exposure stops for snapping
const EXPOSURE_STOPS = [-2, -1.7, -1.3, -1, -0.7, -0.3, 0, 0.3, 0.7, 1, 1.3, 1.7, 2];

// Demo images for preview (when useApi is false)
const demoImages = Array.from({ length: 16 }, (_, i) => ({
    id: `img${i + 1}`,
    filename: `IMG_${String(i + 1).padStart(4, '0')}.CR2`,
    previewUrl: `https://picsum.photos/seed/${i + 1}/1200/800`,
    thumbnailUrl: `https://picsum.photos/seed/${i + 1}/300/200`
}));

// Initialize
async function init() {
    showLoading(true);
    
    // Initialize WebGL exposure renderer
    const canvas = document.getElementById('preview-canvas');
    if (canvas) {
        exposureRenderer = new ExposureRenderer(canvas);
        if (exposureRenderer.supported) {
            console.log('WebGL exposure preview enabled (GPU-accelerated)');
        } else {
            console.log('WebGL not available, using CSS brightness fallback');
        }
    }
    
    try {
        if (state.useApi) {
            // Load images from API
            await loadImagesFromApi();
            // Load edits from API
            await loadEditsFromApi();
        } else {
            // Use demo images
            state.images = demoImages;
            // Load saved edits from localStorage
            const saved = localStorage.getItem('camera-edits');
            if (saved) {
                state.edits = JSON.parse(saved);
            }
        }
    } catch (error) {
        console.error('Failed to load from API, falling back to demo mode:', error);
        state.useApi = false;
        state.images = demoImages;
        const saved = localStorage.getItem('camera-edits');
        if (saved) {
            state.edits = JSON.parse(saved);
        }
    }
    
    // Render the grid with loaded images
    renderImageGrid();
    updateStats();
    document.getElementById('total-count').textContent = state.images.length;
    
    // Apply B&W filters to thumbnails based on saved state
    updateAllThumbnailFilters();
    
    // Apply any active filter
    applyFilter();
    
    showLoading(false);
}

// Load images from API
async function loadImagesFromApi() {
    const response = await fetch('/api/images');
    if (!response.ok) {
        throw new Error(`Failed to load images: ${response.status}`);
    }
    state.images = await response.json();
}

// Load edits from API
async function loadEditsFromApi() {
    try {
        const response = await fetch('/api/edits');
        if (response.ok) {
            const data = await response.json();
            state.edits = data.edits || {};
            state.globalPreset = data.globalPreset || 'color';
            // Update global preset buttons
            document.getElementById('global-bw').classList.toggle('active', state.globalPreset === 'bw');
            document.getElementById('global-color').classList.toggle('active', state.globalPreset === 'color');
        }
    } catch (error) {
        console.log('No saved edits found, starting fresh');
        state.edits = {};
    }
}

// Render image grid dynamically
function renderImageGrid() {
    const grid = document.getElementById('image-grid');
    grid.innerHTML = ''; // Clear existing content
    
    state.images.forEach((img, index) => {
        const item = document.createElement('div');
        item.className = `grid-item ${getGridItemClass(index)}`;
        item.onclick = () => openModal(index);
        
        const imgEl = document.createElement('img');
        imgEl.src = img.thumbnailUrl;
        imgEl.alt = img.filename;
        imgEl.loading = 'lazy'; // Lazy load for performance
        
        const filename = document.createElement('span');
        filename.className = 'filename';
        filename.textContent = img.filename;
        
        item.appendChild(imgEl);
        item.appendChild(filename);
        grid.appendChild(item);
    });
}

// Show/hide loading overlay
function showLoading(show) {
    state.isLoading = show;
    let overlay = document.getElementById('loading-overlay');
    if (!overlay && show) {
        overlay = document.createElement('div');
        overlay.id = 'loading-overlay';
        overlay.innerHTML = '<div class="loading-spinner"></div><div>Loading images...</div>';
        document.body.appendChild(overlay);
    }
    if (overlay) {
        overlay.style.display = show ? 'flex' : 'none';
    }
}

// Auto-save on any change
function saveEdits() {
    // Always save to localStorage for fast local persistence
    localStorage.setItem('camera-edits', JSON.stringify(state.edits));
    updateStats();
    updateGridItem(state.currentIndex);
    
    // Also save to server API if available
    if (state.useApi) {
        saveEditsToApi();
    }
}

// Debounced save to API to avoid too many requests
let saveTimeout = null;
function saveEditsToApi() {
    if (saveTimeout) {
        clearTimeout(saveTimeout);
    }
    saveTimeout = setTimeout(async () => {
        try {
            const payload = {
                globalPreset: state.globalPreset,
                edits: state.edits
            };
            const response = await fetch('/api/edits', {
                method: 'POST',
                headers: {
                    'Content-Type': 'application/json'
                },
                body: JSON.stringify(payload)
            });
            if (!response.ok) {
                console.error('Failed to save edits to server');
            }
        } catch (error) {
            console.error('Error saving edits:', error);
        }
    }, 500); // Debounce 500ms
}

function getEdit(index) {
    const img = state.images[index];
    if (!state.edits[img.id]) {
        state.edits[img.id] = {
            exposure: 0,
            rotation: 0,
            crop: null,
            aspect: 'free',
            bw: state.globalPreset === 'bw',
            skip: false,
            touched: false
        };
    }
    const edit = state.edits[img.id];
    // Normalize: treat full-image crop as no crop
    if (edit.crop && edit.crop.x === 0 && edit.crop.y === 0 &&
        edit.crop.width === 1 && edit.crop.height === 1) {
        edit.crop = null;
    }
    return edit;
}

function markTouched() {
    const edit = getEdit(state.currentIndex);
    if (!edit.touched) {
        edit.touched = true;
        saveEdits();
    }
}

// Grid
function getGridItemClass(index) {
    const edit = state.edits[state.images[index]?.id];
    if (!edit) return '';
    if (edit.skip) return 'skipped';
    if (edit.touched) return 'edited';
    return '';
}

function updateGridItem(index) {
    const items = document.querySelectorAll('.grid-item');
    if (items[index]) {
        const classes = ['grid-item', getGridItemClass(index)];
        // Preserve filter-hidden class based on current filter
        const edit = state.edits[state.images[index]?.id];
        const isEdited = edit && edit.touched && !edit.skip;
        const isSkipped = edit && edit.skip;
        let visible = true;
        switch (state.currentFilter) {
            case 'edited': visible = isEdited; break;
            case 'unedited': visible = !isEdited && !isSkipped; break;
            case 'skipped': visible = isSkipped; break;
        }
        if (!visible) classes.push('filter-hidden');
        items[index].className = classes.join(' ');
        
        // Apply B&W filter to thumbnail
        const img = items[index].querySelector('img');
        if (img) {
            const isBW = edit ? edit.bw : (state.globalPreset === 'bw');
            img.style.filter = isBW ? 'grayscale(1)' : '';
        }
    }
}

function updateStats() {
    const editedCount = Object.values(state.edits).filter(e => e.touched && !e.skip).length;
    document.getElementById('edited-count').textContent = editedCount;
}

// Filter functions
function setFilter(filter) {
    state.currentFilter = filter;
    // Update filter button states
    document.querySelectorAll('.filter-controls button').forEach(btn => {
        btn.classList.toggle('active', btn.dataset.filter === filter);
    });
    applyFilter();
}

function applyFilter() {
    const items = document.querySelectorAll('.grid-item');
    items.forEach((item, i) => {
        if (!state.images[i]) return;
        const edit = state.edits[state.images[i].id];
        const isEdited = edit && edit.touched && !edit.skip;
        const isSkipped = edit && edit.skip;

        let visible = true;
        switch (state.currentFilter) {
            case 'edited':
                visible = isEdited;
                break;
            case 'unedited':
                visible = !isEdited && !isSkipped;
                break;
            case 'skipped':
                visible = isSkipped;
                break;
            default: // 'all'
                visible = true;
        }
        item.classList.toggle('filter-hidden', !visible);
    });
}

// Reset all edits back to original settings
function resetAllEdits() {
    const editedCount = Object.values(state.edits).filter(e => e.touched).length;
    if (editedCount === 0) {
        return; // Nothing to reset
    }
    
    if (!confirm(`Reset all ${editedCount} modified image(s) back to original settings?\n\nThis cannot be undone.`)) {
        return;
    }
    
    // Clear all edits
    state.edits = {};
    
    // Re-render grid to update visual indicators
    renderImageGrid();
    updateAllThumbnailFilters();
    updateStats();
    applyFilter();
    
    // Save the cleared state
    localStorage.setItem('camera-edits', JSON.stringify(state.edits));
    if (state.useApi) {
        saveEditsToApi();
    }
}

// Get camera aspect ratio from current image EXIF data
// Uses server-computed displayAspectRatio which accounts for EXIF orientation
function getCameraAspectRatio() {
    const img = state.images[state.currentIndex];
    if (!img) return 4/3;
    
    // Use display aspect ratio (already orientation-adjusted by server)
    if (img.displayAspectRatio) {
        return parseAspectRatioString(img.displayAspectRatio);
    }
    if (img.aspectRatio) {
        return parseAspectRatioString(img.aspectRatio);
    }
    return 4/3; // Default for OM System cameras (4:3 sensor)
}

// Get the display-oriented aspect ratio STRING for the current image
// Uses server-computed displayAspectRatio (no JS orientation detection needed)
function getDisplayAspectRatioString() {
    const img = state.images[state.currentIndex];
    if (!img) return null;
    return img.displayAspectRatio || img.aspectRatio || null;
}

// Parse aspect ratio string like "16:9" or "3:4" into numeric ratio
function parseAspectRatioString(ratioStr) {
    const parts = ratioStr.split(':');
    if (parts.length === 2) {
        const w = parseFloat(parts[0]);
        const h = parseFloat(parts[1]);
        if (!isNaN(w) && !isNaN(h) && h > 0) {
            return w / h;
        }
    }
    return 4/3; // Default fallback
}

// Check if current image has a non-default camera aspect ratio
// Returns true for images where the camera set a crop different from the native 4:3 sensor
// Uses displayAspectRatio for orientation-aware comparison
function hasCustomCameraAspect() {
    const img = state.images[state.currentIndex];
    if (!img) return false;
    
    const displayRatio = img.displayAspectRatio || img.aspectRatio;
    if (!displayRatio) return false;
    
    // 4:3 and 3:4 are the native sensor format (landscape/portrait)
    if (displayRatio === '4:3' || displayRatio === '3:4') return false;
    
    return true;
}

// Standard aspect ratio buttons available in the UI
const standardAspectButtons = new Set([
    'free', 'original', '1:1', '3:2', '2:3', '4:3', '3:4',
    '16:9', '9:16', '5:4', '4:5'
]);

// Update camera aspect ratio UI elements
function updateCameraAspectUI(img) {
    const labelEl = document.getElementById('camera-aspect-label');
    const valueEl = document.getElementById('camera-aspect-value');
    const btnEl = document.getElementById('camera-aspect-btn');
    
    if (hasCustomCameraAspect()) {
        const displayRatio = img.displayAspectRatio || img.aspectRatio || '4:3';
        
        // Only show camera button if ratio doesn't match a standard button
        if (standardAspectButtons.has(displayRatio)) {
            // Standard ratio - no need for camera label/button, standard button suffices
            labelEl.style.display = 'none';
            btnEl.style.display = 'none';
        } else {
            // Non-standard ratio - show camera button
            labelEl.style.display = 'inline';
            btnEl.style.display = 'inline-block';
            valueEl.textContent = displayRatio;
            btnEl.textContent = '📷 ' + displayRatio;
            btnEl.title = 'Apply camera aspect ratio: ' + displayRatio;
        }
    } else {
        // Hide camera aspect ratio elements for 4:3 images (native sensor format)
        labelEl.style.display = 'none';
        btnEl.style.display = 'none';
    }
}

// Modal
function openModal(index) {
    state.currentIndex = index;
    const img = state.images[index];
    const edit = getEdit(index);
    
    document.getElementById('modal').classList.add('active');
    document.getElementById('modal-filename').textContent = img.filename;
    
    const previewImg = document.getElementById('preview-image');
    previewImg.src = img.previewUrl;
    
    // Invalidate WebGL texture so it re-uploads the new image
    if (exposureRenderer) {
        exposureRenderer.invalidate();
    }
    
    // Set controls
    document.getElementById('exposure').value = edit.exposure;
    document.getElementById('exposure-value').textContent = formatEV(edit.exposure);
    document.getElementById('rotation').value = edit.rotation;
    document.getElementById('rotation-value').textContent = edit.rotation.toFixed(1) + '°';
    
    updateBWToggle(edit.bw);
    updateExposurePresets(edit.exposure);
    
    // Update camera aspect ratio UI - uses server-side data, no image load needed
    updateCameraAspectUI(img);
    
    // Set aspect ratio button - uses server-computed displayAspectRatio (no race condition)
    if (edit.touched && edit.crop) {
        // Image was edited AND has a real crop - restore saved state
        let savedAspect = edit.aspect || 'free';
        // Map 'camera' to actual display ratio if camera button is hidden
        if (savedAspect === 'camera') {
            savedAspect = img.displayAspectRatio || img.aspectRatio || 'free';
        }
        updateAspectButtons(savedAspect);
    } else {
        // Untouched image OR no actual crop - auto-select the correct aspect ratio button
        const displayRatio = getDisplayAspectRatioString();
        if (displayRatio) {
            edit.aspect = displayRatio;
            updateAspectButtons(displayRatio);
        } else {
            edit.aspect = 'free';
            updateAspectButtons('free');
        }
    }
    
    // Handle crop overlay
    // Only show crop overlay for images the user has actually edited (touched)
    // Untouched images may have stale crop data from previous sessions - ignore it
    document.getElementById('crop-overlay').classList.remove('active');
    
    if (!edit.touched) {
        // Clear any stale crop data from old sessions for untouched images
        edit.crop = null;
    }
    
    if (edit.touched && edit.crop) {
        const openIndex = index;
        const onImageReady = () => {
            if (state.currentIndex !== openIndex) return;
            document.getElementById('crop-overlay').classList.add('active');
            restoreCropArea(edit);
        };
        
        if (previewImg.complete && previewImg.naturalWidth > 0) {
            onImageReady();
        } else {
            previewImg.addEventListener('load', onImageReady, { once: true });
        }
    }
    
    applyPreview(edit);
    
    // Show grid lines and rotation crop overlay only when rotating
    if (edit.rotation !== 0) {
        document.getElementById('grid-lines').classList.add('visible');
        document.getElementById('rotation-crop-overlay').classList.add('visible');
        if (previewImg.complete && previewImg.naturalWidth > 0) {
            updateRotationCropPreview(edit.rotation);
        }
    } else {
        document.getElementById('grid-lines').classList.remove('visible');
        document.getElementById('rotation-crop-overlay').classList.remove('visible');
    }
}

function closeModal() {
    document.getElementById('modal').classList.remove('active');
    // Reset preview visibility for next open
    const canvas = document.getElementById('preview-canvas');
    const img = document.getElementById('preview-image');
    if (canvas) canvas.style.display = 'none';
    if (img) {
        img.style.visibility = 'visible';
        img.style.filter = '';
    }
}

function prevImage() {
    if (state.currentIndex > 0) {
        openModal(state.currentIndex - 1);
    }
}

function nextImage() {
    if (state.currentIndex < state.images.length - 1) {
        openModal(state.currentIndex + 1);
    }
}

// Exposure Controls
function setExposure(value) {
    const edit = getEdit(state.currentIndex);
    edit.exposure = parseFloat(value);
    document.getElementById('exposure').value = edit.exposure;
    document.getElementById('exposure-value').textContent = formatEV(edit.exposure);
    updateExposurePresets(edit.exposure);
    markTouched();
    applyPreview(edit);
    saveEdits();
}

function updateExposure(value) {
    const edit = getEdit(state.currentIndex);
    
    // Snap to nearest stop if close enough
    const numValue = parseFloat(value);
    const snappedValue = snapToStop(numValue);
    
    edit.exposure = snappedValue;
    document.getElementById('exposure-value').textContent = formatEV(edit.exposure);
    updateExposurePresets(edit.exposure);
    markTouched();
    applyPreview(edit);
    saveEdits();
}

function snapToStop(value) {
    // Snap to nearest 1/3 stop if within 0.05
    for (const stop of EXPOSURE_STOPS) {
        if (Math.abs(value - stop) < 0.05) {
            return stop;
        }
    }
    return Math.round(value * 10) / 10; // Round to 1 decimal
}

function updateExposurePresets(value) {
    document.querySelectorAll('.ev-btn').forEach(btn => {
        const btnValue = parseFloat(btn.dataset.ev);
        btn.classList.toggle('active', Math.abs(btnValue - value) < 0.05);
    });
}

function resetExposure() {
    setExposure(0);
}

// Rotation Controls
function updateRotation(value) {
    const edit = getEdit(state.currentIndex);
    edit.rotation = parseFloat(value);
    document.getElementById('rotation-value').textContent = edit.rotation.toFixed(1) + '°';
    
    // Show grid and crop preview while rotating
    const isRotated = edit.rotation !== 0;
    document.getElementById('grid-lines').classList.toggle('visible', isRotated);
    document.getElementById('rotation-crop-overlay').classList.toggle('visible', isRotated);
    
    // Update crop preview to show what will be cut
    if (isRotated) {
        updateRotationCropPreview(edit.rotation);
    }
    
    markTouched();
    applyPreview(edit);
    saveEdits();
}

function resetRotation() {
    const edit = getEdit(state.currentIndex);
    edit.rotation = 0;
    document.getElementById('rotation').value = 0;
    document.getElementById('rotation-value').textContent = '0°';
    document.getElementById('grid-lines').classList.remove('visible');
    document.getElementById('rotation-crop-overlay').classList.remove('visible');
    applyPreview(edit);
    saveEdits();
}

// Calculate the inscribed rectangle after rotation
// When an image is rotated, we need to crop to avoid showing empty corners
function updateRotationCropPreview(angleDegrees) {
    const img = document.getElementById('preview-image');
    const overlay = document.getElementById('rotation-crop-overlay');
    const cropBox = document.getElementById('rotation-crop-box');
    const container = document.getElementById('preview-container');
    
    if (!img.complete || img.naturalWidth === 0) return;
    
    const angleRad = Math.abs(angleDegrees) * Math.PI / 180;
    const sinA = Math.sin(angleRad);
    const cosA = Math.cos(angleRad);
    
    // Get image dimensions (before rotation)
    const imgWidth = img.offsetWidth;
    const imgHeight = img.offsetHeight;
    
    // Get positions relative to container
    const containerRect = container.getBoundingClientRect();
    const imgRect = img.getBoundingClientRect();
    
    // Image center position relative to container
    const imgCenterX = imgRect.left + imgRect.width / 2 - containerRect.left;
    const imgCenterY = imgRect.top + imgRect.height / 2 - containerRect.top;
    
    // Calculate the inscribed rectangle that fits inside the rotated image
    // When a rectangle (w x h) is rotated by angle θ, the largest inscribed
    // axis-aligned rectangle with the SAME aspect ratio has dimensions:
    //
    // For the corners of the inscribed rectangle to touch the edges of the
    // rotated original rectangle, we need to scale down such that:
    // new_w = w * cos(θ) - h * sin(θ) (won't work for all cases)
    //
    // Better approach: calculate how much the inscribed rectangle needs to
    // shrink so its corners fit within the rotated parallelogram
    
    let cropWidth, cropHeight;
    
    if (angleRad < 0.001) {
        cropWidth = imgWidth;
        cropHeight = imgHeight;
    } else {
        // The inscribed rectangle with same aspect ratio as original
        // For angle θ, the scale factor is:
        // s = 1 / (cos(θ) + sin(θ) * max(w/h, h/w))
        // But we want same aspect ratio, so:
        // s = cos(θ) - sin(θ) * tan(θ) for the limiting dimension
        
        // Simpler correct formula:
        // The width of inscribed rect = (W * cos(θ) - H * |sin(θ)|) when W > H
        // But to maintain aspect ratio, we use a uniform scale:
        
        const w = imgWidth;
        const h = imgHeight;
        
        // Maximum inscribed rectangle maintaining original aspect ratio
        // Scale factor: the inscribed rect corners must be within the rotated rect
        // For a point at (±w/2, ±h/2) in the inscribed rect,
        // after considering rotation of the outer rect, we need:
        // |x * cos(θ) + y * sin(θ)| <= W/2
        // |x * sin(θ) - y * cos(θ)| <= H/2
        
        // For inscribed rect with half-dims (sw/2, sh/2):
        // (sw/2) * cos(θ) + (sh/2) * sin(θ) <= w/2
        // (sw/2) * sin(θ) + (sh/2) * cos(θ) <= h/2
        
        // With s as scale factor and maintaining aspect ratio (sw = s*w, sh = s*h):
        // s * (w * cos(θ) + h * sin(θ)) <= w  →  s <= w / (w*cos + h*sin)
        // s * (w * sin(θ) + h * cos(θ)) <= h  →  s <= h / (w*sin + h*cos)
        
        const s1 = w / (w * cosA + h * sinA);
        const s2 = h / (w * sinA + h * cosA);
        const scaleFactor = Math.min(s1, s2);
        
        cropWidth = w * scaleFactor;
        cropHeight = h * scaleFactor;
    }
    
    // Center the crop box on the image center
    const cropLeft = imgCenterX - cropWidth / 2;
    const cropTop = imgCenterY - cropHeight / 2;
    
    // Apply to the crop box (positioned relative to the full container overlay)
    cropBox.style.left = cropLeft + 'px';
    cropBox.style.top = cropTop + 'px';
    cropBox.style.width = cropWidth + 'px';
    cropBox.style.height = cropHeight + 'px';
}

// Update crop preview when image loads
document.getElementById('preview-image').addEventListener('load', function() {
    if (!state.images || !state.images[state.currentIndex]) return;
    const edit = getEdit(state.currentIndex);
    if (edit.rotation !== 0) {
        updateRotationCropPreview(edit.rotation);
    }
});

// Crop Controls
function setAspect(aspect) {
    const edit = getEdit(state.currentIndex);
    edit.aspect = aspect;
    state.currentAspect = aspect;
    updateAspectButtons(aspect);
    
    // Show crop overlay for all modes (including free)
    document.getElementById('crop-overlay').classList.add('active');
    initializeCropArea(aspect);
    markTouched();
    
    saveEdits();
}

function initializeCropArea(aspect) {
    const img = document.getElementById('preview-image');
    const container = document.getElementById('preview-container');
    const cropArea = document.getElementById('crop-area');
    
    if (!img.complete) return;
    
    // Get image position relative to container
    const containerRect = container.getBoundingClientRect();
    const imgRect = img.getBoundingClientRect();
    
    const imgLeft = imgRect.left - containerRect.left;
    const imgTop = imgRect.top - containerRect.top;
    const imgWidth = imgRect.width;
    const imgHeight = imgRect.height;
    
    // Calculate crop dimensions based on aspect ratio
    let cropWidth, cropHeight;
    const imgAspect = imgWidth / imgHeight;
    
    const aspectRatios = {
        'free': imgAspect,  // Free starts at full image
        '1:1': 1,
        '3:2': 3/2,
        '2:3': 2/3,
        '4:3': 4/3,
        '3:4': 3/4,
        '16:9': 16/9,
        '9:16': 9/16,
        '5:4': 5/4,
        '4:5': 4/5,
        '7:6': 7/6,
        '6:7': 6/7,
        '6:5': 6/5,
        '5:6': 5/6,
        '7:5': 7/5,
        '5:7': 5/7,
        'original': imgAspect,
        'camera': getCameraAspectRatio()  // Use EXIF aspect ratio from camera
    };
    
    const ratio = aspectRatios[aspect] || imgAspect;
    
    // Fit the crop area to fill as much of the image as possible
    if (imgAspect > ratio) {
        // Image is wider than target ratio - height is the constraint
        cropHeight = imgHeight;
        cropWidth = cropHeight * ratio;
    } else {
        // Image is taller than target ratio - width is the constraint
        cropWidth = imgWidth;
        cropHeight = cropWidth / ratio;
    }
    
    // Center the crop area on the image
    const cropLeft = imgLeft + (imgWidth - cropWidth) / 2;
    const cropTop = imgTop + (imgHeight - cropHeight) / 2;
    
    // Apply to crop area
    cropArea.style.left = cropLeft + 'px';
    cropArea.style.top = cropTop + 'px';
    cropArea.style.width = cropWidth + 'px';
    cropArea.style.height = cropHeight + 'px';
    
    // Store crop data
    const edit = getEdit(state.currentIndex);
    edit.crop = {
        x: (cropLeft - imgLeft) / imgWidth,
        y: (cropTop - imgTop) / imgHeight,
        width: cropWidth / imgWidth,
        height: cropHeight / imgHeight
    };
}

// Restore crop area from saved edit data
function restoreCropArea(edit) {
    if (!edit.crop) return;
    
    const img = document.getElementById('preview-image');
    const container = document.getElementById('preview-container');
    const cropArea = document.getElementById('crop-area');
    
    if (!img.complete) return;
    
    // Get image position relative to container
    const containerRect = container.getBoundingClientRect();
    const imgRect = img.getBoundingClientRect();
    
    const imgLeft = imgRect.left - containerRect.left;
    const imgTop = imgRect.top - containerRect.top;
    const imgWidth = imgRect.width;
    const imgHeight = imgRect.height;
    
    // Convert normalized values back to pixels
    const cropLeft = imgLeft + (edit.crop.x * imgWidth);
    const cropTop = imgTop + (edit.crop.y * imgHeight);
    const cropWidth = edit.crop.width * imgWidth;
    const cropHeight = edit.crop.height * imgHeight;
    
    cropArea.style.left = cropLeft + 'px';
    cropArea.style.top = cropTop + 'px';
    cropArea.style.width = cropWidth + 'px';
    cropArea.style.height = cropHeight + 'px';
}

function updateAspectButtons(aspect) {
    document.querySelectorAll('.aspect-buttons button').forEach(btn => {
        btn.classList.toggle('active', btn.dataset.aspect === aspect);
    });
}

function resetCrop() {
    const edit = getEdit(state.currentIndex);
    edit.crop = null;
    edit.aspect = 'free';
    updateAspectButtons('free');
    state.currentAspect = 'free';
    document.getElementById('crop-overlay').classList.remove('active');
    saveEdits();
}

// Crop drag functionality
let cropDragState = null;

function getImageBounds() {
    const img = document.getElementById('preview-image');
    const container = document.getElementById('preview-container');
    const containerRect = container.getBoundingClientRect();
    const imgRect = img.getBoundingClientRect();
    
    return {
        left: imgRect.left - containerRect.left,
        top: imgRect.top - containerRect.top,
        width: imgRect.width,
        height: imgRect.height,
        right: imgRect.left - containerRect.left + imgRect.width,
        bottom: imgRect.top - containerRect.top + imgRect.height
    };
}

function initCropDragHandlers() {
    const cropArea = document.getElementById('crop-area');
    const cropOverlay = document.getElementById('crop-overlay');
    
    // Handle mousedown on crop area (move) and handles (resize)
    cropArea.addEventListener('mousedown', (e) => {
        e.preventDefault();
        const handle = e.target.dataset.handle;
        const rect = cropArea.getBoundingClientRect();
        
        cropDragState = {
            handle: handle || 'move',
            startX: e.clientX,
            startY: e.clientY,
            startLeft: cropArea.offsetLeft,
            startTop: cropArea.offsetTop,
            startWidth: cropArea.offsetWidth,
            startHeight: cropArea.offsetHeight,
            imgBounds: getImageBounds()
        };
    });
    
    document.addEventListener('mousemove', (e) => {
        if (!cropDragState) return;
        
        const dx = e.clientX - cropDragState.startX;
        const dy = e.clientY - cropDragState.startY;
        const cropArea = document.getElementById('crop-area');
        const bounds = cropDragState.imgBounds;
        
        if (cropDragState.handle === 'move') {
            // Calculate new position
            let newLeft = cropDragState.startLeft + dx;
            let newTop = cropDragState.startTop + dy;
            
            // Constrain to image boundaries
            newLeft = Math.max(bounds.left, Math.min(bounds.right - cropArea.offsetWidth, newLeft));
            newTop = Math.max(bounds.top, Math.min(bounds.bottom - cropArea.offsetHeight, newTop));
            
            cropArea.style.left = newLeft + 'px';
            cropArea.style.top = newTop + 'px';
        } else {
            // Handle resize
            let newLeft = cropDragState.startLeft;
            let newTop = cropDragState.startTop;
            let newWidth = cropDragState.startWidth;
            let newHeight = cropDragState.startHeight;
            
            if (cropDragState.handle.includes('r')) {
                newWidth = Math.max(50, cropDragState.startWidth + dx);
                // Constrain right edge to image boundary
                const maxWidth = bounds.right - newLeft;
                newWidth = Math.min(newWidth, maxWidth);
            }
            if (cropDragState.handle.includes('l')) {
                const potentialWidth = cropDragState.startWidth - dx;
                const potentialLeft = cropDragState.startLeft + dx;
                // Constrain left edge to image boundary
                if (potentialLeft >= bounds.left && potentialWidth >= 50) {
                    newWidth = potentialWidth;
                    newLeft = potentialLeft;
                } else if (potentialLeft < bounds.left) {
                    newLeft = bounds.left;
                    newWidth = cropDragState.startLeft + cropDragState.startWidth - bounds.left;
                }
            }
            if (cropDragState.handle.includes('b')) {
                newHeight = Math.max(50, cropDragState.startHeight + dy);
                // Constrain bottom edge to image boundary
                const maxHeight = bounds.bottom - newTop;
                newHeight = Math.min(newHeight, maxHeight);
            }
            if (cropDragState.handle.includes('t')) {
                const potentialHeight = cropDragState.startHeight - dy;
                const potentialTop = cropDragState.startTop + dy;
                // Constrain top edge to image boundary
                if (potentialTop >= bounds.top && potentialHeight >= 50) {
                    newHeight = potentialHeight;
                    newTop = potentialTop;
                } else if (potentialTop < bounds.top) {
                    newTop = bounds.top;
                    newHeight = cropDragState.startTop + cropDragState.startHeight - bounds.top;
                }
            }
            
            // Maintain aspect ratio if not free
            if (state.currentAspect !== 'free') {
                const aspectRatios = {
                    '1:1': 1,
                    '3:2': 3/2,
                    '2:3': 2/3,
                    '4:3': 4/3,
                    '3:4': 3/4,
                    '16:9': 16/9,
                    '9:16': 9/16,
                    '5:4': 5/4,
                    '4:5': 4/5,
                    '7:6': 7/6,
                    '6:7': 6/7,
                    '6:5': 6/5,
                    '5:6': 5/6,
                    '7:5': 7/5,
                    '5:7': 5/7,
                    'original': cropDragState.startWidth / cropDragState.startHeight,
                    'camera': getCameraAspectRatio()
                };
                const ratio = aspectRatios[state.currentAspect] || (cropDragState.startWidth / cropDragState.startHeight);
                
                // Adjust height based on width, then check bounds
                if (cropDragState.handle.includes('r') || cropDragState.handle.includes('l')) {
                    newHeight = newWidth / ratio;
                    // Check if height exceeds bounds
                    if (newTop + newHeight > bounds.bottom) {
                        newHeight = bounds.bottom - newTop;
                        newWidth = newHeight * ratio;
                    }
                } else {
                    newWidth = newHeight * ratio;
                    // Check if width exceeds bounds
                    if (newLeft + newWidth > bounds.right) {
                        newWidth = bounds.right - newLeft;
                        newHeight = newWidth / ratio;
                    }
                }
            }
            
            cropArea.style.left = newLeft + 'px';
            cropArea.style.top = newTop + 'px';
            cropArea.style.width = newWidth + 'px';
            cropArea.style.height = newHeight + 'px';
        }
        
        updateCropData();
    });
    
    document.addEventListener('mouseup', () => {
        cropDragState = null;
    });
}

function updateCropData() {
    const img = document.getElementById('preview-image');
    const container = document.getElementById('preview-container');
    const cropArea = document.getElementById('crop-area');
    const edit = getEdit(state.currentIndex);
    
    const containerRect = container.getBoundingClientRect();
    const imgRect = img.getBoundingClientRect();
    
    const imgLeft = imgRect.left - containerRect.left;
    const imgTop = imgRect.top - containerRect.top;
    const imgWidth = imgRect.width;
    const imgHeight = imgRect.height;
    
    edit.crop = {
        x: (cropArea.offsetLeft - imgLeft) / imgWidth,
        y: (cropArea.offsetTop - imgTop) / imgHeight,
        width: cropArea.offsetWidth / imgWidth,
        height: cropArea.offsetHeight / imgHeight
    };
    
    saveEdits();
}

// Initialize crop handlers when DOM is ready
document.addEventListener('DOMContentLoaded', () => {
    initCropDragHandlers();
});

// B&W Toggle
function setBW(isBW) {
    const edit = getEdit(state.currentIndex);
    edit.bw = isBW;
    updateBWToggle(isBW);
    markTouched();
    applyPreview(edit);
    saveEdits();
    updateGridItem(state.currentIndex); // Update thumbnail filter
}

function updateBWToggle(isBW) {
    document.getElementById('bw-btn').classList.toggle('active', isBW);
    document.getElementById('color-btn').classList.toggle('active', !isBW);
}

// Reset All
function resetEdit() {
    const img = state.images[state.currentIndex];
    state.edits[img.id] = {
        exposure: 0,
        rotation: 0,
        crop: null,
        aspect: 'free',
        bw: state.globalPreset === 'bw',
        skip: false,
        touched: false
    };
    
    document.getElementById('exposure').value = 0;
    document.getElementById('exposure-value').textContent = '0';
    updateExposurePresets(0);
    document.getElementById('rotation').value = 0;
    document.getElementById('rotation-value').textContent = '0°';
    document.getElementById('grid-lines').classList.remove('visible');
    document.getElementById('rotation-crop-overlay').classList.remove('visible');
    document.getElementById('crop-overlay').classList.remove('active');
    
    updateBWToggle(state.globalPreset === 'bw');
    updateAspectButtons('free');
    applyPreview(state.edits[img.id]);
    saveEdits();
}

function skipImage() {
    const edit = getEdit(state.currentIndex);
    edit.skip = true;
    edit.touched = true;
    saveEdits();
    nextImage();
}

// Global preset
function setGlobalPreset(preset) {
    state.globalPreset = preset;
    document.getElementById('global-bw').classList.toggle('active', preset === 'bw');
    document.getElementById('global-color').classList.toggle('active', preset === 'color');
    
    // Apply to all non-touched images
    state.images.forEach((img, i) => {
        const edit = state.edits[img.id];
        if (!edit || !edit.touched) {
            if (!state.edits[img.id]) {
                state.edits[img.id] = getEdit(i);
            }
            state.edits[img.id].bw = preset === 'bw';
        }
    });
    saveEdits();
    
    // Update all thumbnail filters
    updateAllThumbnailFilters();
}

function updateAllThumbnailFilters() {
    const items = document.querySelectorAll('.grid-item');
    items.forEach((item, i) => {
        const img = item.querySelector('img');
        if (img && state.images[i]) {
            const edit = state.edits[state.images[i].id];
            const isBW = edit ? edit.bw : (state.globalPreset === 'bw');
            img.style.filter = isBW ? 'grayscale(1)' : '';
        }
    });
}

// Live preview using WebGL for accurate exposure simulation
// Falls back to CSS filters if WebGL is not available.
//
// WebGL approach:
// - Converts sRGB pixels to linear light space
// - Applies 2^EV multiplier in linear space (matching RawTherapee behavior)
// - Uses filmic highlight rolloff (Reinhard tonemapping) to prevent hard clipping
// - Converts back to sRGB for display
//
// CSS fallback:
// - Uses brightness() which operates in gamma space (less accurate)
// - Highlights clip harshly to white
function applyPreview(edit) {
    const img = document.getElementById('preview-image');
    const canvas = document.getElementById('preview-canvas');

    if (exposureRenderer && exposureRenderer.supported) {
        // WebGL GPU path
        const doRender = () => {
            exposureRenderer.loadImage(img);
            exposureRenderer.render(edit.exposure, edit.bw);

            // Size canvas display to match the <img> element's rendered size
            canvas.style.width = img.offsetWidth + 'px';
            canvas.style.height = img.offsetHeight + 'px';

            // Show canvas, hide img (img stays in DOM for layout/crop calculations)
            canvas.style.display = 'block';
            img.style.visibility = 'hidden';
            img.style.filter = '';
        };

        if (img.complete && img.naturalWidth > 0) {
            doRender();
        } else {
            img.addEventListener('load', doRender, { once: true });
        }

        // Rotation still uses CSS transform on both elements
        canvas.style.transform = `rotate(${edit.rotation}deg)`;
        img.style.transform = `rotate(${edit.rotation}deg)`;
    } else {
        // CSS fallback (original behavior)
        canvas.style.display = 'none';
        img.style.visibility = 'visible';

        const filters = [];
        const brightness = Math.pow(2, edit.exposure);
        filters.push(`brightness(${brightness})`);

        if (edit.bw) {
            filters.push('grayscale(1)');
            filters.push('contrast(1.1)');
        }

        img.style.filter = filters.join(' ');
        img.style.transform = `rotate(${edit.rotation}deg)`;
    }
}

function formatEV(value) {
    if (value === 0) return '0';
    if (value > 0) return '+' + value.toFixed(1);
    return value.toFixed(1);
}

// Keyboard shortcuts
document.addEventListener('keydown', (e) => {
    if (!document.getElementById('modal').classList.contains('active')) return;
    
    // Check if we're in an input field
    const isInInput = e.target.tagName === 'INPUT' || e.target.tagName === 'SELECT';
    
    switch (e.key) {
        case 'ArrowLeft':
            e.preventDefault();
            e.stopPropagation();
            // Blur any focused element to prevent slider interference
            if (document.activeElement) document.activeElement.blur();
            prevImage();
            break;
        case 'ArrowRight':
            e.preventDefault();
            e.stopPropagation();
            // Blur any focused element to prevent slider interference
            if (document.activeElement) document.activeElement.blur();
            nextImage();
            break;
        case 'ArrowUp':
            e.preventDefault();
            e.stopPropagation();
            // Blur slider if focused, then adjust exposure
            if (document.activeElement) document.activeElement.blur();
            adjustExposureByStep(0.3); // Move up by 1/3 stop
            break;
        case 'ArrowDown':
            e.preventDefault();
            e.stopPropagation();
            // Blur slider if focused, then adjust exposure
            if (document.activeElement) document.activeElement.blur();
            adjustExposureByStep(-0.3); // Move down by 1/3 stop
            break;
        case 'b':
        case 'B':
            if (!isInInput) {
                const edit = getEdit(state.currentIndex);
                setBW(!edit.bw);
            }
            break;
        case 'r':
        case 'R':
            if (!isInInput) {
                resetEdit();
            }
            break;
        case 'Escape':
            closeModal();
            break;
    }
}, true); // Use capture phase to intercept before sliders

function adjustExposureByStep(step) {
    const current = parseFloat(document.getElementById('exposure').value);
    const newValue = Math.max(-2, Math.min(2, current + step));
    const snapped = snapToStop(newValue);
    document.getElementById('exposure').value = snapped;
    setExposure(snapped);
}

// Adjust rotation by a fine step (±0.1°)
function adjustRotationByStep(step) {
    const current = parseFloat(document.getElementById('rotation').value);
    const newValue = Math.max(-15, Math.min(15, Math.round((current + step) * 10) / 10));
    document.getElementById('rotation').value = newValue;
    updateRotation(newValue);
}

// Process
async function processAll() {
    // Count only images that are currently loaded (not old edits from previous sessions)
    const currentImageIds = new Set(state.images.map(img => img.id));
    const imagesToProcess = state.images.filter(img => {
        const edit = state.edits[img.id];
        return !edit || !edit.skip;
    });
    const count = imagesToProcess.length;
    
    if (!state.useApi) {
        alert(`Would process ${count} images.\n\nIn production, this sends edits to the server for RawTherapee processing.\n\nExposure adjustments will be applied via the PP3 [Exposure] Compensation parameter, which works on RAW data for full quality.`);
        return;
    }
    
    // Start processing immediately without confirmation
    showLoading(true);
    document.getElementById('loading-overlay').querySelector('div:last-child').textContent = `Processing ${count} images...`;
    
    try {
        // Save edits first
        await fetch('/api/edits', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({
                globalPreset: state.globalPreset,
                edits: state.edits
            })
        });
        
        // Trigger processing
        const response = await fetch('/api/process', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' }
        });
        
        if (!response.ok) {
            throw new Error(`Processing failed: ${response.status}`);
        }
        
        const result = await response.json();
        
        // Update status to show completion
        const successCount = result.processed || 0;
        const errorCount = result.errors || 0;
        
        document.getElementById('loading-overlay').querySelector('div:last-child').textContent =
            `✓ ${successCount} images processed. Shutting down...`;
        
        // Wait a moment to show the message, then trigger shutdown
        setTimeout(async () => {
            try {
                await fetch('/api/shutdown', { method: 'POST' });
            } catch (e) {
                // Server will close, so this request may fail - that's expected
            }
            // Close the browser tab/window
            window.close();
        }, 1500);
        
    } catch (error) {
        console.error('Processing error:', error);
        alert(`Processing failed: ${error.message}`);
        showLoading(false);
    }
}

// Initialize on load
document.addEventListener('DOMContentLoaded', init);