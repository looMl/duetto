import type { Variant } from './protocol';

export interface VariantSupport {
  variant: Variant;
  supported: boolean;
  smooth: boolean;
}

/**
 * Ask the browser whether it can decode each variant smoothly. The server
 * lists variants best-first, so the first smooth one wins. HDR variants also
 * require an HDR-capable display; otherwise the SDR fallback looks better.
 */
export async function rankVariants(variants: Variant[]): Promise<VariantSupport[]> {
  const hdrDisplay = matchMedia('(dynamic-range: high)').matches;
  return Promise.all(
    variants.map(async (variant) => {
      const videoCodec = variant.codecs.split(',')[0]!.trim();
      const contentType = `video/mp4; codecs="${videoCodec}"`;
      try {
        const info = await navigator.mediaCapabilities.decodingInfo({
          type: 'file',
          video: {
            contentType,
            width: variant.width || 1920,
            height: variant.height || 1080,
            bitrate: variant.bitrate || 8_000_000,
            framerate: 24,
            ...(variant.hdr
              ? { transferFunction: 'pq', colorGamut: 'rec2020', hdrMetadataType: 'smpteSt2086' }
              : {}),
          } as VideoConfiguration,
        });
        const ok = info.supported && (!variant.hdr || hdrDisplay);
        return { variant, supported: ok, smooth: ok && info.smooth };
      } catch {
        // Browsers that reject the HDR fields throw: fall back to canPlayType.
        const ok = document.createElement('video').canPlayType(contentType) !== '' && !variant.hdr;
        return { variant, supported: ok, smooth: ok };
      }
    }),
  );
}

export function pickVariant(ranked: VariantSupport[]): Variant | null {
  return (
    ranked.find((r) => r.smooth)?.variant ??
    ranked.find((r) => r.supported)?.variant ??
    ranked[ranked.length - 1]?.variant ??
    null
  );
}
