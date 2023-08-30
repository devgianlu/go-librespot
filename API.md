# API

The API is divided in multiple REST endpoints for controlling and a WebSocket endpoint for events.

## REST

The REST API documentation is available as OpenAPI specification: [api-spec.yml](api-spec.yml).

## WebSocket

The WebSocket endpoint is available at `/events`.
The following events are emitted:

- `active`: The device has become active
- `inactive`: The device has become inactive
- `track`: A new track was loaded, the following data is provided:
  - `uri`: Track URI
  - `name`: Track name
  - `artist_names`: List of track artist names
  - `album_name`: Track album name
  - `album_cover_url`: Track album cover image URL
  - `position`: Track position in milliseconds
  - `duration`: Track duration in milliseconds
- `playing`: The current track is playing
  - `play_origin`: Who started the playback
- `paused`: The current track is paused
  - `play_origin`: Who started the playback
- `seek`: The current track was seeked, the following data is provided:
  - `position`: Track position in milliseconds
  - `duration`: Track duration in milliseconds
  - `play_origin`: Who started the playback
- `volume`: The player volume changed, the following data is provided:
  - `value`: The volume, ranging from 0 to max
  - `max`: The max volume value
