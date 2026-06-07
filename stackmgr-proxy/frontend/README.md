# StackMgr Frontend - SSG for Cloudflare Pages

Next.js Static Site Generation (SSG) frontend for stack management dashboard.

## Features

- Modern React UI with TypeScript
- Tailwind CSS styling
- Responsive design optimized for desktop and mobile
- Real-time stack status updates
- Auth integration with auth.ab18.in

## Environment Variables

- `NEXT_PUBLIC_API_URL`: Backend API URL (default: http://localhost:8080)
- `NEXT_PUBLIC_AUTH_URL`: Auth service URL (default: https://auth.ab18.in)

## Development

```bash
npm install
npm run dev
```

Access at http://localhost:3000

## Building for Cloudflare Pages

```bash
npm run build
```

The static output will be in the `out/` directory.

## Deployment to Cloudflare Pages

1. Push code to GitHub
2. Connect repository to Cloudflare Pages
3. Set build command: `npm run build`
4. Set publish directory: `out`
5. Set environment variables in Cloudflare Pages settings:
   - `NEXT_PUBLIC_API_URL`: Your backend API URL
   - `NEXT_PUBLIC_AUTH_URL`: Your auth service URL (default: https://auth.ab18.in)

## File Structure

```
frontend/
├── src/
│   ├── app/           # Next.js app directory
│   ├── components/    # React components
│   ├── lib/          # Utilities and API client
│   └── types/        # TypeScript types
├── public/           # Static assets
└── package.json      # Dependencies
```
