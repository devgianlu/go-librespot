# API documentation

The API has multiple REST endpoints and a Websocket endpoint.

## REST

The REST API documentation is available as OpenAPI specification: [api-spec.yml](/api-spec.yml).

## Websocket

The websocket endpoint is available at `/events`. The following events are emitted:

- `active`: The device has become active
- `inactive`: The device has become inactive
- `metadata`: A new track was loaded, the following metadata is available:
    - `uri`: Track URI
    - `name`: Track name
    - `artist_names`: List of track artist names
    - `album_name`: Track album name
    - `album_cover_url`: Track album cover image URL
    - `position`: Track position in milliseconds
    - `duration`: Track duration in milliseconds
- `will_play`: The player is about to play the specified track
    - `uri`: The track URI
    - `play_origin`: Who started the playback
- `playing`: The current track is playing
    - `uri`: The track URI
    - `play_origin`: Who started the playback
- `not_playing`: The current track has finished playing
    - `uri`: The track URI
    - `play_origin`: Who started the playback
- `paused`: The current track is paused
    - `uri`: The track URI
    - `play_origin`: Who started the playback
- `stopped`: The current context is empty, nothing more to play
    - `play_origin`: Who started the playback
- `seek`: The current track was seeked, the following data is provided:
    - `uri`: The track URI
    - `position`: Track position in milliseconds
    - `duration`: Track duration in milliseconds
    - `play_origin`: Who started the playback
- `volume`: The player volume changed, the following data is provided:
    - `value`: The volume, ranging from 0 to max
    - `max`: The max volume value
- `shuffle_context`: The player shuffling context setting changed
    - `value`: Whether shuffling context is enabled
- `repeat_context`: The player repeating context setting changed
    - `value`: Whether repeating context is enabled
- `repeat_track`: The player repeating track setting changed
    - `value`: Whether repeating track is enabled
