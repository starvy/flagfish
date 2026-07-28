import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import Globe, { type GlobeMethods } from "react-globe.gl";
import { MeshPhongMaterial } from "three";
import { COUNTRY_FEATURES, codeOfFeature } from "./countries";
import { indexByCode, type CountryState } from "./countryStates";
import { anchorOf, nameOf } from "./geography";
import { altitudeOf, capColorOf, SCENE, strokeColorOf } from "./palette";
import type { Pulse } from "./usePulses";

/** One pin. A country in play gets exactly one, however many challenges are in it. */
interface Pin {
  code: string;
  lat: number;
  lng: number;
  label: string;
  count: number;
  solved: number;
  capture: CountryState["capture"];
}

export interface GlobeSceneProps {
  countries: readonly CountryState[];
  pulses: readonly Pulse[];
  /** Called with a country code when its pin is clicked. */
  onSelectCountry: (code: string) => void;
  /** The country the camera should be looking at, or null to leave it where the player put it. */
  focus: string | null;
}

const IDLE_BEFORE_SPIN_MS = 5_000;
const SPIN_SPEED = 0.32;
const FOCUS_ALTITUDE = 1.5;

export function GlobeScene({ countries, pulses, onSelectCountry, focus }: GlobeSceneProps) {
  const globe = useRef<GlobeMethods | undefined>(undefined);
  const frame = useRef<HTMLDivElement>(null);
  const size = useElementSize(frame);

  const byCode = useMemo(() => indexByCode(countries), [countries]);

  const pins = useMemo<Pin[]>(() => {
    const out: Pin[] = [];
    for (const country of countries) {
      const at = anchorOf(country.code);
      if (at === null) continue; // placeChallenges already filtered these; belt and braces
      out.push({
        code: country.code,
        lat: at.lat,
        lng: at.lng,
        // A country holding exactly one challenge names it: there is nothing to disambiguate,
        // so the pin may as well say what the player is about to open.
        label: country.total === 1 ? country.challenges[0]!.name : nameOf(country.code),
        count: country.total,
        solved: country.solved,
        capture: country.capture,
      });
    }
    return out;
  }, [countries]);

  // The globe re-creates a pin's element whenever the data changes, so the handler has to come
  // from a ref — a captured one would go stale the first time the board refetched.
  const select = useRef(onSelectCountry);
  select.current = onSelectCountry;

  const pinElement = useCallback((datum: object) => {
    const pin = datum as Pin;
    const el = document.createElement("button");
    el.type = "button";
    el.className = `globe-pin globe-pin--${pin.capture}`;
    el.dataset.testid = `globe-pin-${pin.code}`;
    el.setAttribute(
      "aria-label",
      `${nameOf(pin.code)}: ${pin.count} ${pin.count === 1 ? "challenge" : "challenges"}, ${pin.solved} solved`,
    );
    el.innerHTML =
      `<span class="globe-pin__stem"></span>` +
      `<span class="globe-pin__body">` +
      `<span class="globe-pin__label"></span>` +
      (pin.count > 1 ? `<span class="globe-pin__count">${pin.count}</span>` : "") +
      `</span>`;
    // textContent, never innerHTML: the label is a challenge name, which an operator wrote.
    el.querySelector(".globe-pin__label")!.textContent = pin.label;
    el.addEventListener("click", (e) => {
      e.stopPropagation();
      select.current(pin.code);
    });
    return el;
  }, []);

  // A dark, unlit sphere. No texture: the only earth image on offer would come off a CDN, and
  // nothing in this app is loaded from one.
  const globeMaterial = useMemo(
    () => new MeshPhongMaterial({ color: "#081420", emissive: "#04101a", shininess: 4 }),
    [],
  );

  const spin = useAutoRotate(globe, frame);

  useEffect(() => {
    if (focus === null) return;
    const at = anchorOf(focus);
    if (at === null) return;
    spin.suspend();
    globe.current?.pointOfView({ lat: at.lat, lng: at.lng, altitude: FOCUS_ALTITUDE }, 900);
  }, [focus, spin]);

  return (
    <div className="globe-scene" ref={frame} data-testid="globe-scene">
      <div className="globe-scene__sky" aria-hidden="true" />
      {size !== null && (
        <Globe
          ref={globe}
          width={size.width}
          height={size.height}
          backgroundColor={SCENE.background}
          // Explicitly nothing: both default to a remote texture in the upstream examples.
          globeImageUrl={null}
          backgroundImageUrl={null}
          globeMaterial={globeMaterial}
          showAtmosphere
          atmosphereColor={SCENE.atmosphere}
          atmosphereAltitude={0.22}
          polygonsData={COUNTRY_FEATURES as unknown as object[]}
          polygonCapColor={(d: object) => capColorOf(byCode.get(codeOfFeature(d)))}
          polygonSideColor={() => "rgba(20, 40, 60, 0.45)"}
          polygonStrokeColor={(d: object) => strokeColorOf(byCode.get(codeOfFeature(d)))}
          polygonAltitude={(d: object) => altitudeOf(byCode.get(codeOfFeature(d)))}
          polygonsTransitionDuration={400}
          polygonLabel={(d: object) => labelFor(byCode.get(codeOfFeature(d)), codeOfFeature(d))}
          onPolygonClick={(d: object) => {
            const code = codeOfFeature(d);
            if (byCode.has(code)) select.current(code);
          }}
          htmlElementsData={pins as unknown as object[]}
          htmlLat="lat"
          htmlLng="lng"
          htmlAltitude={0.06}
          htmlElement={pinElement}
          htmlTransitionDuration={300}
          ringsData={pulses as unknown as object[]}
          ringColor={(d: object) => ringColorOf(d as Pulse)}
          ringMaxRadius={(d: object) => ((d as Pulse).firstBlood ? 7 : 4)}
          ringPropagationSpeed={2.2}
          ringRepeatPeriod={(d: object) => ((d as Pulse).firstBlood ? 500 : 900)}
          onGlobeReady={spin.start}
        />
      )}
    </div>
  );
}

function labelFor(state: CountryState | undefined, code: string): string {
  const name = nameOf(code);
  if (state === undefined) return name;
  return `${name} — ${state.solved}/${state.total} solved`;
}

// A ring fades as it travels. `t` runs 0→1 across one propagation.
function ringColorOf(pulse: Pulse): (t: number) => string {
  const [r, g, b] = pulse.firstBlood ? [255, 77, 109] : [34, 211, 238];
  return (t: number) => `rgba(${r},${g},${b},${(1 - t).toFixed(3)})`;
}

interface Size {
  width: number;
  height: number;
}

/** The canvas is sized in pixels, so the frame has to be measured rather than styled. */
function useElementSize(ref: React.RefObject<HTMLElement | null>): Size | null {
  const [size, setSize] = useState<Size | null>(null);

  useEffect(() => {
    const node = ref.current;
    if (node === null) return;
    const observer = new ResizeObserver(([entry]) => {
      const box = entry!.contentRect;
      setSize({ width: Math.round(box.width), height: Math.round(box.height) });
    });
    observer.observe(node);
    return () => observer.disconnect();
  }, [ref]);

  return size;
}

interface Spin {
  start: () => void;
  suspend: () => void;
}

/**
 * Idles by turning slowly, and gets out of the way the moment the player touches it.
 *
 * Rotation resumes only after a stretch of quiet — a globe that starts spinning again while
 * someone is still reading a pin is a globe that fights its user.
 */
function useAutoRotate(
  globe: React.RefObject<GlobeMethods | undefined>,
  frame: React.RefObject<HTMLElement | null>,
): Spin {
  const resume = useRef<ReturnType<typeof setTimeout> | null>(null);
  const wanted = useRef(false);

  const apply = useCallback(
    (on: boolean) => {
      const controls = globe.current?.controls();
      if (controls === undefined) return;
      controls.autoRotate = on;
      controls.autoRotateSpeed = SPIN_SPEED;
    },
    [globe],
  );

  const suspend = useCallback(() => {
    apply(false);
    if (resume.current !== null) clearTimeout(resume.current);
    resume.current = setTimeout(() => {
      resume.current = null;
      if (wanted.current) apply(true);
    }, IDLE_BEFORE_SPIN_MS);
  }, [apply]);

  const start = useCallback(() => {
    wanted.current = true;
    apply(true);
  }, [apply]);

  useEffect(() => {
    const node = frame.current;
    if (node === null) return;
    const onInteract = () => suspend();
    node.addEventListener("pointerdown", onInteract);
    node.addEventListener("wheel", onInteract, { passive: true });
    return () => {
      node.removeEventListener("pointerdown", onInteract);
      node.removeEventListener("wheel", onInteract);
      if (resume.current !== null) clearTimeout(resume.current);
    };
  }, [frame, suspend]);

  return useMemo(() => ({ start, suspend }), [start, suspend]);
}
