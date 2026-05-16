export interface DocEntry {
  slug: string
  title: string
  description: string
  category: string
  order: number
  updatedAt: string
  source: string
}

interface Frontmatter {
  title?: string
  description?: string
  category?: string
  order?: number
  updatedAt?: string
}

const modules = import.meta.glob('./*.mdx', {
  eager: true,
  query: '?raw',
  import: 'default',
}) as Record<string, string>

function slugFromPath(path: string): string {
  return path.replace(/^\.\/(.*)\.mdx$/, '$1')
}

function parseFrontmatter(source: string): { data: Frontmatter; body: string } {
  if (!source.startsWith('---')) {
    return { data: {}, body: source }
  }

  const end = source.indexOf('\n---', 3)
  if (end === -1) {
    return { data: {}, body: source }
  }

  const raw = source.slice(3, end).trim()
  const body = source.slice(end + 4).trimStart()
  const data: Frontmatter = {}

  for (const line of raw.split('\n')) {
    const separator = line.indexOf(':')
    if (separator === -1) {
      continue
    }
    const key = line.slice(0, separator).trim()
    const value = line.slice(separator + 1).trim().replace(/^['"]|['"]$/g, '')
    if (key === 'order') {
      const order = Number(value)
      data.order = Number.isFinite(order) ? order : undefined
    } else if (key === 'title') {
      data.title = value
    } else if (key === 'description') {
      data.description = value
    } else if (key === 'category') {
      data.category = value
    } else if (key === 'updatedAt') {
      data.updatedAt = value
    }
  }

  return { data, body }
}

export const docs: DocEntry[] = Object.entries(modules)
  .map(([path, source]) => {
    const slug = slugFromPath(path)
    const { data, body } = parseFrontmatter(source)
    return {
      slug,
      title: data.title || slug,
      description: data.description || '',
      category: data.category || 'Guides',
      order: data.order ?? 100,
      updatedAt: data.updatedAt || '',
      source: body,
    }
  })
  .sort((a, b) => a.order - b.order || a.title.localeCompare(b.title))

export function getDoc(slug: string | string[] | undefined): DocEntry | undefined {
  const normalized = Array.isArray(slug) ? slug[0] : slug
  return docs.find((doc) => doc.slug === normalized) || docs[0]
}
