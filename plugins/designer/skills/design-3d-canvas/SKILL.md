---
name: design-3d-canvas
description: Web-native 3D graphics, procedural geometries, and custom GLSL vertex/fragment shaders without Blender or heavy external runtimes.
---

# Web-Native 3D & Canvas Shaders Runbook

## 1. Executive Summary & Zero-Blender Philosophy

Traditional 3D pipelines often depend on heavy desktop DCC software (Blender, Maya) and multi-megabyte binary exports (`.glb`, `.fbx`). In the Antigravity developer ecosystem, the Designer agent adopts a **Zero-Blender, Web-Native Standard**:

1. **Pure WebGL & Canvas**: Deliver high-impact 3D visuals using standard browser WebGL 1.0/2.0 or lightweight inline procedural shaders.
2. **Mathematical Proceduralism**: Geometry and surfaces are synthesized dynamically via code (trigonometric harmonics, procedural noise, signed distance functions) rather than static polygon meshes.
3. **Hardware Acceleration with Low Footprint**: Shaders run directly on the client GPU with virtually zero download overhead (<50 KB single-file deliverables).
4. **Adaptive Performance**: Dynamic level-of-detail (LOD) and step clamping to maintain locked 60fps across low-end and high-end GPUs.

---

## 2. GLSL Shader Architecture & Standard Uniforms

When generating procedural 3D canvases, fragment shader raymarching is the preferred approach for self-contained single-file deliverables.

### Standard Uniforms Contract
Every procedural shader canvas must supply the following uniforms:

| Uniform | Type | Description |
| :--- | :--- | :--- |
| `u_resolution` | `vec2` | Canvas physical dimensions in pixels (`width, height`) |
| `u_time` | `float` | Elapsed execution time in seconds since start |
| `u_mouse` | `vec2` | Normalized mouse coordinates (`[0..1], [0..1]`) |

### Raymarching Signed Distance Functions (SDF)
Standard procedural primitives:

```glsl
// Signed distance to a sphere
float sdSphere(vec3 p, float r) {
  return length(p) - r;
}

// Signed distance to a torus
float sdTorus(vec3 p, vec2 t) {
  vec2 q = vec2(length(p.xz) - t.x, p.y);
  return length(q) - t.y;
}

// Smooth minimum for organic blending
float smin(float a, float b, float k) {
  float h = clamp(0.5 + 0.5 * (b - a) / k, 0.0, 1.0);
  return mix(b, a, h) - k * h * (1.0 - h);
}
```

---

## 3. Production Runtime Standards

### Throttled Resize Handling
Never re-allocate WebGL framebuffers on every subpixel scroll or micro-resize. Use a debounced or `requestAnimationFrame`-throttled resize routine:

```javascript
let resizePending = false;
const resizeObserver = new ResizeObserver(() => {
  if (!resizePending) {
    resizePending = true;
    requestAnimationFrame(() => {
      const dpr = Math.min(window.devicePixelRatio || 1, 2); // Cap at 2x to avoid GPU starvation
      canvas.width = Math.floor(canvas.clientWidth * dpr);
      canvas.height = Math.floor(canvas.clientHeight * dpr);
      gl.viewport(0, 0, canvas.width, canvas.height);
      resizePending = false;
    });
  }
});
resizeObserver.observe(canvas);
```

### Context Loss & Restoration
Prevent black screens when the GPU driver restarts or suspends:

```javascript
canvas.addEventListener('webglcontextlost', (event) => {
  event.preventDefault(); // Prevent default engine crash
  cancelAnimationFrame(animationFrameId);
  console.warn('[WebGL] Context lost. Pausing render loop.');
}, false);

canvas.addEventListener('webglcontextrestored', () => {
  console.info('[WebGL] Context restored. Re-initializing shaders.');
  initShadersAndBuffers();
  requestAnimationFrame(renderLoop);
}, false);
```

### 60 FPS Delta-Time Normalization
```javascript
let then = 0;
function render(now) {
  now *= 0.001; // convert to seconds
  const deltaTime = now - then;
  then = now;

  gl.uniform1f(uTimeLocation, now);
  gl.drawArrays(gl.TRIANGLES, 0, 6);

  requestAnimationFrame(render);
}
requestAnimationFrame(render);
```
