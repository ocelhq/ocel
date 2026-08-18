import {
  serveImage as routeImage,
  type ImageCache,
  type ImageDeps as RouterImageDeps,
} from "@framework/next-router/image";

import { answerableImageRequest, serveCachedImage, type CacheDeps } from "./cache";
import {
  durableImageOrigin,
  durableImageRefresh,
  imageObjectKey,
  type ImageStore,
} from "./image-store";

export interface ImageColoDeps {
  slug: string;
  cache?: CacheDeps;
  imageStore?: ImageStore;
}

export type ImageDeps = Omit<RouterImageDeps, "imageCache"> & {
  cache?: CacheDeps;
  imageStore?: ImageStore;
};

export function coloImageCache(deps: ImageColoDeps): ImageCache {
  return (context) => {
    let readThrough = context.origin;
    let refresh = context.origin;
    if (deps.imageStore && deps.cache && answerableImageRequest(context.request)) {
      const objectKey = imageObjectKey(deps.slug, context.digest);
      readThrough = durableImageOrigin(
        deps.imageStore,
        deps.cache,
        objectKey,
        context.origin,
      );
      refresh = context.absolute
        ? durableImageRefresh(deps.imageStore, deps.cache, objectKey, context.origin)
        : readThrough;
    }

    return serveCachedImage(
      context.request,
      { key: context.key },
      deps.cache,
      readThrough,
      refresh,
      context.servedCacheControl,
    );
  };
}

export function serveImage(
  request: Request,
  url: URL,
  deps: ImageDeps,
): Promise<Response> {
  return routeImage(request, url, {
    ...deps,
    imageCache: coloImageCache(deps),
  });
}
